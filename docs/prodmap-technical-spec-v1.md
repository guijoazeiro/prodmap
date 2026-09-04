# Prodmap — Technical Specification v1

> Especificação normativa para implementação do núcleo do Prodmap.

**Relacionada a:** `prodmap-product-spec-v2.md`  
**Status:** proposta inicial  
**Linguagem:** Go  
**Escopo:** Phases 0–4

---

## 1. Como interpretar este documento

As palavras **DEVE**, **NÃO DEVE**, **DEVERIA** e **PODE** são normativas:

- **DEVE/NÃO DEVE:** requisito obrigatório;
- **DEVERIA:** padrão esperado; exceções exigem ADR;
- **PODE:** decisão local permitida.

Em caso de conflito:

1. segurança e integridade dos dados;
2. esta especificação técnica;
3. especificação de produto;
4. convenções locais de implementação.

Uma decisão que altere domínio, persistência, contrato público ou semântica de confiança DEVE ser registrada em ADR antes da implementação.

---

# Parte I — Architecture Decision Records

## ADR-001 — Monólito modular organizado por capacidades

**Status:** aceito

O Prodmap DEVE começar como um único binário e um único módulo Go, organizado por capacidades de produto. A estrutura de diretórios NÃO DEVE impor camadas horizontais globais como `domain`, `application`, `ports`, `adapters`, `repositories` ou `storage`.

```text
cmd/prodmap/
internal/
  inventory/
  correlation/
  investigation/
  git/
  docker/
  sqlite/
  cli/
  config/
  logging/
  clock/

# Criados somente quando a capacidade for implementada:
  deployment/
  telemetry/
  regression/
  otel/
  github/
```

Regras:

- tipos e regras ficam próximos da capacidade que os possui;
- `inventory` pode possuir `Artifact`, `RuntimeInstance` e os contratos necessários para inspecioná-los;
- `correlation` pode possuir `Evidence`, `Correlation` e políticas de confidence;
- `regression` pode possuir `Baseline`, `BehaviorChange` e `RegressionCandidate`;
- packages de integração como `git`, `docker`, `otel`, `github` e `sqlite` convertem modelos externos para os contratos das capacidades;
- capacidades não importam SDKs de fornecedores nem tipos de persistência;
- parsing de CLI permanece em `cli`;
- packages futuros NÃO DEVEM ser criados vazios apenas para materializar o desenho;
- pacotes `util`, `common`, `helpers` e `models` genéricos NÃO DEVEM ser criados.
- ciclos de importação NÃO DEVEM ser resolvidos movendo tudo para um pacote genérico.

**Razão:** organização por capacidade mantém comportamento, tipos e contratos coesos e evita arquitetura cerimonial sem necessidade demonstrada.

## ADR-002 — SQLite como armazenamento inicial

**Status:** aceito

SQLite será o armazenamento padrão nas Phases 0–4.

O driver inicial é `modernc.org/sqlite`, fixado no módulo. Ele foi escolhido para manter o binário e os testes portáveis sem exigir CGO; trocar o driver exige revisar compatibilidade, licença, pragmas e comportamento transacional.

Requisitos:

- foreign keys habilitadas;
- WAL habilitado;
- `busy_timeout` configurado;
- timestamps armazenados em UTC como texto RFC3339Nano;
- IDs armazenados como texto;
- valores monetários não fazem parte do domínio inicial;
- durações armazenadas em nanossegundos inteiros;
- percentuais e scores armazenados como `REAL`, validados entre 0 e 1;
- escrita serializada pela implementação SQLite;
- leitura concorrente permitida;
- queries críticas cobertas por integration tests.

As capacidades NÃO DEVEM depender de tipos específicos de SQLite.

## ADR-003 — Migrações append-only

**Status:** aceito

Migrações serão embutidas no binário e executadas em transação exclusiva durante a abertura do banco.

Convenção:

```text
000001_initial_schema.sql
000002_add_evidence_subject.sql
```

Regras:

- migração aplicada NÃO DEVE ser alterada;
- checksum de cada migração aplicada DEVE ser persistido;
- downgrade automático não será suportado inicialmente;
- falha interrompe o startup sem executar comandos de negócio;
- migração destrutiva exige backup automático e ADR específico;
- testes DEVEM cobrir banco vazio e upgrade desde cada versão suportada.

## ADR-004 — CLI com stdout estável e stderr operacional

**Status:** aceito

- resultados vão para `stdout`;
- progresso, warnings e diagnóstico vão para `stderr`;
- `--json` emite um único documento JSON válido em `stdout`;
- cores são desabilitadas em `--json` e quando stdout não é TTY;
- prompts interativos NÃO DEVEM ocorrer sem `--interactive`;
- comandos de leitura não alteram fontes externas;
- nomes e flags seguem kebab-case.

Exit codes:

```text
0  sucesso
1  falha interna ou não classificada
2  uso inválido/validação
3  configuração inválida
4  dependência indisponível
5  dados insuficientes
6  conflito/invariante
7  acesso negado
8  versão ou schema incompatível
```

Resultados legítimos como “nenhuma regressão encontrada” retornam `0`. Dados insuficientes para executar a análise retornam `5`.

## ADR-005 — Configuração em camadas explícitas

**Status:** aceito

Precedência, da maior para a menor:

```text
flags CLI
variáveis PRODMAP_*
arquivo do projeto
arquivo do usuário
defaults compilados
```

O arquivo usa YAML. Campos desconhecidos causam erro. Configuração efetiva pode ser exibida com segredos redigidos.

O parser inicial é `go.yaml.in/yaml/v3`, fixado no módulo, por oferecer uma API pequena, mantida e decodificação estrita sem introduzir um framework de configuração. Trocar o parser ou relaxar campos desconhecidos exige atualizar esta decisão.

Paths padrão:

```text
project: .prodmap/config.yaml
data:    .prodmap/prodmap.db
user:    $XDG_CONFIG_HOME/prodmap/config.yaml
```

O programa DEVE respeitar XDG. Caminhos podem ser sobrescritos. Segredos NÃO DEVEM ser aceitos no arquivo; devem vir de variável de ambiente ou referência a secret store futura.

## ADR-006 — Logging estruturado com `log/slog`

**Status:** aceito

- usar `log/slog`;
- formato padrão humano em TTY e JSON quando configurado;
- níveis: debug, info, warn, error;
- logs incluem `operation_id`, `source` e duração quando aplicável;
- mensagens são estáveis o suficiente para operação, mas não são API;
- tokens, headers, DSNs e payloads NÃO DEVEM ser registrados;
- bibliotecas recebem logger; não usam logger global.

## ADR-007 — Erros tipados e preservação de causa

**Status:** aceito

Erros do domínio usam sentinel errors somente para categorias examináveis:

```go
var (
	ErrInvalid       = errors.New("invalid input")
	ErrNotFound      = errors.New("not found")
	ErrUnavailable   = errors.New("dependency unavailable")
	ErrInsufficient  = errors.New("insufficient data")
	ErrConflict      = errors.New("conflict")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrIncompatible  = errors.New("incompatible version")
)
```

Regras:

- envolver causa com `%w`;
- mensagem deve acrescentar operação e entidade;
- NÃO comparar texto de erro;
- package de integração traduz erro externo para categoria interna;
- CLI é a única camada que converte erro em exit code;
- erro parcial informa itens processados, rejeitados e causa por item;
- `panic` apenas para invariante impossível durante desenvolvimento; nunca por input externo.

## ADR-008 — IDs e tempo

**Status:** aceito

- entidades internas usam UUIDv7 textual;
- IDs externos são preservados separadamente com source e namespace;
- todo tempo interno é UTC;
- limites de intervalo seguem `[start, end)`;
- `observed_at` indica quando o fato ocorreu na fonte;
- `ingested_at` indica quando o Prodmap recebeu o fato;
- operações sensíveis ao tempo recebem uma interface `Clock` injetável.

## ADR-009 — Transações e idempotência

**Status:** aceito

- cada lote de uma source é persistido atomicamente;
- chave `(source_id, external_id)` garante idempotência quando houver ID externo;
- sem ID externo, a integração gera fingerprint determinístico documentado;
- reingestão pode atualizar campos mutáveis, mas não apaga histórico válido;
- conflito de identidade é registrado e não mesclado silenciosamente;
- correlações derivadas registram versão do algoritmo e IDs das evidências.

## ADR-010 — Versionamento de contratos

**Status:** aceito

- JSON público contém `schema_version`;
- formato inicial: `1.0`;
- adição de campo opcional incrementa minor;
- remoção ou mudança semântica incrementa major;
- consumers DEVEM ignorar campos desconhecidos na mesma major;
- schemas JSON ficam versionados no repositório e têm golden tests.

## ADR-011 — Interfaces pertencem ao consumidor

**Status:** aceito

Interfaces DEVEM ser definidas preferencialmente pelo package que consome o comportamento, ser pequenas e representar somente o necessário para um caso de uso.

```go
package inventory

type RuntimeSource interface {
	InspectRuntime(ctx context.Context) ([]RuntimeObservation, error)
}

type ArtifactStore interface {
	SaveArtifacts(ctx context.Context, artifacts []Artifact) error
	ArtifactByDigest(ctx context.Context, algorithm, digest string) (Artifact, error)
}
```

Regras:

- NÃO criar interface apenas para envolver uma implementação concreta;
- NÃO criar package global `ports`;
- NÃO criar interfaces CRUD universais;
- métodos devem expressar intenção do caso de uso, como `RuntimeAt` ou `ArtifactByDigest`, e não apenas `Create`, `Update`, `Delete` e `FindAll`;
- uma implementação concreta pode satisfazer interfaces de mais de uma capacidade;
- abstrações surgem quando um consumidor real precisa delas, não por antecipação.

## ADR-012 — Modelos de domínio, persistência e transporte são distintos

**Status:** aceito

Uma única struct NÃO DEVE ser usada simultaneamente como objeto de domínio, registro de banco e contrato público JSON.

Regras:

- cada capacidade possui seus tipos de domínio;
- `sqlite` possui rows e mapeamentos privados quando necessários;
- `cli` e MCP possuem DTOs próprios e versionados;
- conversões ocorrem nas fronteiras;
- tags de banco e JSON não devem contaminar tipos de domínio por conveniência;
- diferenças justificadas entre representações são esperadas, principalmente para temporalidade, nullability, redaction e valores derivados.

## ADR-013 — Schema implementado de forma incremental

**Status:** aceito

O schema completo deste documento é um mapa conceitual e contratual. Ele NÃO autoriza a criação antecipada de todas as tabelas.

Uma entidade só entra em migration quando uma vertical slice implementada precisar persistir seus dados. A migration inicial DEVE conter somente o necessário para a Phase 1. Interfaces de armazenamento são específicas dos casos de uso existentes e não devem antecipar CRUD de entidades futuras.

## ADR-014 — Hot reload local versionado

**Status:** aceito

O ambiente de desenvolvimento DEVE oferecer hot reload antes da implementação das capacidades do produto.

Regras:

- Air é a ferramenta inicial de live reload;
- a versão da ferramenta é fixada em `go.mod` por meio da diretiva `tool`;
- o comando canônico é `make dev`, que executa `go tool air -c .air.toml`;
- a configuração versionada compila exclusivamente `./cmd/prodmap`;
- binários temporários ficam em `.tmp/`, que não é versionado; o Air é configurado para removê-los em encerramentos normais;
- somente builds bem-sucedidos substituem o processo em execução;
- o processo anterior recebe sinal de interrupção antes do encerramento forçado;
- hot reload é recurso exclusivo de desenvolvimento e NÃO DEVE entrar no binário, imagem ou runtime de produção;
- alterar a ferramenta ou seu contrato de uso exige atualizar este ADR e o README.

Critérios de aceite:

- `make dev` funciona em um clone sem instalação global do Air;
- o primeiro build inicia o binário;
- uma alteração válida em arquivo `.go` causa novo build e restart;
- erro de compilação é exibido sem iniciar binário inválido;
- a correção do erro causa recuperação automática;
- encerrar o watcher não deixa processo do Prodmap em execução; `.tmp/` permanece descartável e ignorado caso o terminal force o encerramento antes da limpeza do Air.

## ADRs 015–019 — Exceção e contratos da Phase 2

**Status:** aceitos para a slice de validação

As decisões executáveis estão registradas separadamente para manter o escopo explícito:

- [`ADR-015`](adr/015-phase-2a-experimental-exception.md): Phase 2A e a continuação limitada da Phase 2 para preparar e aprender com o Experimento 001;
- [`ADR-016`](adr/016-otel-ingestion-format.md): OTLP/JSON canônico congelado, parser oficial e ausência de receiver vivo;
- [`ADR-017`](adr/017-topology-identity-cardinality.md): identidades, confidence, janelas, allowlists e limites de cardinalidade.
- [`ADR-018`](adr/018-runtime-observed-service-association.md): associação explícita entre runtime e serviço observado no mesmo environment.
- [`ADR-019`](adr/019-temporal-endpoint-context.md): contexto temporal de endpoints observados, sem agregação de janelas sobrepostas.
- [`ADR-020`](adr/020-offline-deployment-ledger.md): ledger de deployments offline, atômico e sem correlação com runtime.
- [`ADR-021`](adr/021-deployment-runtime-correlation.md): associação deployment/runtime dinâmica, temporal e sem nova migration.
- [`ADR-022`](adr/022-temporal-deployment-timeline.md): timeline temporal dinâmica de deployments e runtimes, sem eventos persistidos.
- [`ADR-023`](adr/023-github-actions-deployment-artifact-source.md): fronteira segura para artifact GitHub Actions, sem CLI ou persistência.
- [`ADR-024`](adr/024-github-actions-deployment-sync.md): sincronização explícita e persistência atômica do ledger autenticado GitHub Actions.

Esses ADRs não autorizam deployments, baseline, regression ou qualquer fase além da conclusão limitada da Phase 2 sob `continue-for-learning`; isso não é `go` e exigiu nova revisão da tese ao final da Phase 2. A [Decision 002](decisions/002-phase-3-limited-learning.md) autoriza separadamente e somente a Phase 3 como aprendizado limitado; ela não é `go`, não valida a tese e mantinha a Phase 4 bloqueada naquele instante.

O [Phase 3 Technical Gate Review](reviews/phase-3-gate-review.md) registra a
avaliação técnica dessa autorização limitada. Ele não substitui a decisão formal
da Phase -1 nem autorizava a Phase 4 naquele instante. A
[Decision 003](decisions/003-phase-4-limited-learning.md) autoriza depois a
Phase 4 apenas como aprendizado limitado, mantendo a Phase 5 bloqueada naquele
instante.
O [Phase 4 Gate Review](reviews/phase-4-gate-review.md) registra o PASS técnico
com limitações e preserva o bloqueio histórico da Phase 5 naquele gate.
A [Decision 004](decisions/004-agent-ready-side-project.md) autoriza depois as
Phases 5 e 6 como evolução técnica limitada: as Slices 5.1 de composição
somente leitura e 5.2 de pacote sanitizado, offline e verificável estão
implementadas, sem fontes brutas, persistência ou migration. O MCP mínimo
somente leitura está autorizado, mas pendente de sua própria slice. Isso não
constitui validação comercial nem decisão formal `go`, `pivot` ou `stop`.

---

# Parte II — Schema conceitual do domínio

O modelo desta parte define vocabulário, invariantes e direção futura. Ele NÃO representa uma ordem de criação de tabelas. Cada entidade indica a fase em que pode se tornar persistência concreta; entidades de fases posteriores permanecem apenas conceituais até uma vertical slice demonstrar sua necessidade.

Para a Phase 1, o conjunto máximo previsto é `Source`, `Repository` quando necessário para desambiguar commits, `Commit`, `Artifact`, `Service` quando necessário para identificar o runtime, `RuntimeInstance`, `Evidence` e `Correlation`. Mesmo dentro desse conjunto, uma tabela só deve existir quando exigida pelo fluxo implementado.

## 2. Convenções comuns

Toda entidade persistida contém:

```text
id            UUIDv7, PK
created_at    timestamp UTC, obrigatório
updated_at    timestamp UTC, obrigatório
```

Toda entidade proveniente de fonte contém ainda:

```text
source_id     FK Source, obrigatório
external_id   string, opcional somente quando fingerprint existir
observed_at   timestamp UTC, obrigatório
ingested_at   timestamp UTC, obrigatório
raw_ref       string, opcional; referência, nunca payload sensível
```

Strings identificadoras são normalizadas em UTF-8, sem espaços nas extremidades. Strings vazias equivalem a ausentes quando o campo for opcional.

## 3. Entidades

### 3.1 Source

**Disponibilidade prevista:** Phase 1.

```text
id              UUIDv7 PK
kind            enum: git|docker|otel|github|manual
name            string(1..128)
instance_key    string(1..512)
config_hash     string SHA-256
last_sync_at    timestamp nullable
last_status     enum: never|success|partial|failed
created_at      timestamp
updated_at      timestamp
```

Constraints e índices:

- UNIQUE `(kind, instance_key)`;
- INDEX `(kind, last_sync_at)`.

### 3.2 Repository

**Disponibilidade prevista:** Phase 1, somente se necessário para desambiguar a identidade de commits. Não é padrão arquitetural nem sinônimo de repository pattern.

```text
id              UUIDv7 PK
source_id       FK Source
external_id     string
name            string(1..255)
canonical_url   string nullable
root_path_hash  string nullable
default_branch  string nullable
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(source_id, external_id)`;
- paths locais completos NÃO DEVEM aparecer em export sanitizado.

### 3.3 Commit

**Disponibilidade prevista:** Phase 1.

```text
id              UUIDv7 PK
repository_id   FK Repository
sha             lowercase hex, 40 ou 64 caracteres
author_time     timestamp nullable
commit_time     timestamp
subject         string(0..1024)
tree_sha        string nullable
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(repository_id, sha)`;
- INDEX `(repository_id, commit_time DESC)`.

### 3.4 Build

**Disponibilidade prevista:** Phase 3. Permanece conceitual nesta slice.

```text
id              UUIDv7 PK
source_id       FK Source
external_id     string
repository_id   FK Repository nullable
commit_id       FK Commit nullable
status          enum: queued|running|succeeded|failed|cancelled|unknown
started_at      timestamp nullable
finished_at     timestamp nullable
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(source_id, external_id)`;
- CHECK `finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at`.

### 3.5 Artifact

**Disponibilidade prevista:** Phase 1.

```text
id              UUIDv7 PK
source_id       FK Source
external_id     string
kind            enum: container_image|binary|archive|other
name            string
digest_algorithm enum: sha256|sha512|other
digest          string
created_by_build_id FK Build nullable
created_at_source timestamp nullable
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(kind, digest_algorithm, digest)`;
- INDEX `(name)`.

Tags são aliases mutáveis e DEVEM ficar em tabela separada `artifact_aliases` com validade temporal.

### 3.6 Deployment

**Disponibilidade:** materializada na Slice 3.1 como deployment ledger offline.
Build permanece conceitual; não há correlação deployment/runtime nesta slice.

```text
id              UUIDv7 PK
source_id       FK Source
external_id     string
environment     string(1..128)
service_key     string(1..255)
artifact_id     FK Artifact nullable
commit_id       FK Commit nullable
status          enum: pending|running|succeeded|failed|cancelled|rolled_back|unknown
strategy        enum: recreate|rolling|blue_green|canary|manual|unknown
started_at      timestamp
finished_at     timestamp nullable
actor_ref       string nullable
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(source_id, external_id)`;
- INDEX `(environment, service_key, started_at DESC)`;
- CHECK de ordem temporal.

### 3.7 Service

**Disponibilidade prevista:** Phase 1 se necessário para o inventário; sua identidade deverá emergir da vertical slice.

```text
id              UUIDv7 PK
logical_key     string(1..255)
environment     string(1..128)
display_name    string(1..255)
first_seen_at   timestamp
last_seen_at    timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(environment, logical_key)`;
- `logical_key` vem de regra configurável e não do display name.

### 3.8 RuntimeInstance

**Disponibilidade prevista:** Phase 1.

```text
id              UUIDv7 PK
source_id       FK Source
external_id     string
service_id      FK Service
artifact_id     FK Artifact nullable
runtime_kind    enum: docker_container|process|pod|other
node_key        string nullable
state           enum: created|running|stopped|failed|unknown
health          enum: healthy|unhealthy|starting|none|unknown
restart_count   integer >= 0
started_at      timestamp nullable
valid_from      timestamp
valid_to        timestamp nullable
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(source_id, external_id, valid_from)`;
- INDEX `(service_id, valid_from DESC)`;
- intervalos da mesma instância NÃO DEVEM se sobrepor;
- `valid_to` é exclusivo.

### 3.9 Endpoint

**Disponibilidade prevista:** Phase 2. Permanece conceitual antes disso.

```text
id              UUIDv7 PK
service_id      FK Service
protocol        enum: http|grpc|messaging|other
operation       string(1..512)
route_template  string nullable
first_seen_at   timestamp
last_seen_at    timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(service_id, protocol, operation)`;
- valores de alta cardinalidade como IDs concretos NÃO DEVEM formar identidade.

### 3.10 Dependency

**Disponibilidade prevista:** Phase 2. Permanece conceitual antes disso.

```text
id              UUIDv7 PK
kind            enum: service|database|cache|queue|external_api|other
logical_key     string(1..512)
display_name    string
first_seen_at   timestamp
last_seen_at    timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE `(kind, logical_key)`.

### 3.11 ServiceDependencyObservation

**Disponibilidade prevista:** Phase 2. Permanece conceitual antes disso.

```text
id              UUIDv7 PK
source_id       FK Source
service_id      FK Service
endpoint_id     FK Endpoint nullable
dependency_id   FK Dependency
window_start    timestamp
window_end      timestamp
request_count   integer >= 0
error_count     integer >= 0
duration_sum_ns integer >= 0
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE por source, relação e janela;
- CHECK `window_end > window_start`;
- `error_count <= request_count`.

### 3.12 TelemetryWindow

**Disponibilidade prevista:** Phase 2/4. Permanece conceitual antes disso.

```text
id              UUIDv7 PK
source_id       FK Source
service_id      FK Service
endpoint_id     FK Endpoint nullable
window_start    timestamp
window_end      timestamp
request_count   integer >= 0
error_count     integer >= 0
latency_p50_ns  integer nullable
latency_p95_ns  integer nullable
latency_p99_ns  integer nullable
coverage_ratio  real 0..1
is_complete     boolean
observed_at     timestamp
ingested_at     timestamp
created_at      timestamp
updated_at      timestamp
```

- UNIQUE por source, alvo e janela;
- percentis DEVEM ser monotônicos quando não nulos;
- INDEX `(service_id, endpoint_id, window_start)`.

### 3.13 Baseline

**Disponibilidade prevista:** Phase 4. A Slice 4.1 implementa somente a consulta
efêmera `previous_window` para service ou endpoint; ela não cria esta entidade
nem persiste baselines, comparações ou regressões.

```text
id                 UUIDv7 PK
target_kind        enum: service|endpoint|dependency
target_id          UUID
metric             enum: request_count|error_rate|latency_p50|latency_p95|latency_p99|dependency_duration
method             enum: previous_window|historical_equivalent|combined
window_start       timestamp
window_end         timestamp
sample_count       integer >= 0
reference_windows  integer >= 1
value              real
dispersion         real nullable
coverage_ratio     real 0..1
confidence_score   real 0..1
confidence_level   enum: high|medium|low|unknown
algorithm_version  string
created_at         timestamp
updated_at         timestamp
```

- INDEX `(target_kind, target_id, metric, window_end DESC)`;
- `EXACT` não é permitido para baseline estatística.

### 3.14 BehaviorChange

**Disponibilidade prevista:** Phase 4. Permanece conceitual antes disso.

```text
id                 UUIDv7 PK
target_kind        enum
target_id          UUID
metric             enum
baseline_id        FK Baseline
observed_window_id FK TelemetryWindow
baseline_value     real
observed_value     real
absolute_delta     real
relative_delta     real nullable
effect_score       real 0..1
direction          enum: increase|decrease
classification     enum: improvement|degradation|neutral|unknown
confidence_score   real 0..1
confidence_level   enum: high|medium|low|unknown
algorithm_version  string
created_at         timestamp
updated_at         timestamp
```

### 3.15 Evidence

**Disponibilidade prevista:** Phase 1.

```text
id                 UUIDv7 PK
kind               enum: identity|temporal|topology|change|exclusivity|data_quality|contradiction
source_id          FK Source nullable
subject_type       string
subject_id         UUID
claim              string(1..1024)
polarity           enum: supports|contradicts|neutral
strength           real 0..1
observed_at        timestamp
details_json       JSON validado e sanitizado
created_at         timestamp
updated_at         timestamp
```

- INDEX `(subject_type, subject_id)`;
- `details_json` NÃO DEVE conter payload bruto, token ou PII não necessária.

### 3.16 Correlation

**Disponibilidade prevista:** Phase 1.

```text
id                 UUIDv7 PK
relation_type      enum: declared|exact|observed|inferred|statistical
from_type          string
from_id            UUID
to_type            string
to_id              UUID
valid_from         timestamp
valid_to           timestamp nullable
score              real 0..1
level              enum: exact|high|medium|low|unknown
algorithm_version  string
explanation        string
created_at         timestamp
updated_at         timestamp
```

- UNIQUE por relação, intervalo e versão do algoritmo;
- `EXACT` exige Evidence `identity` com força 1;
- relações estatísticas NÃO PODEM ter nível `EXACT`.

### 3.17 RegressionCandidate

**Disponibilidade prevista:** Phase 4. Permanece conceitual antes disso.

```text
id                     UUIDv7 PK
behavior_change_id     FK BehaviorChange
deployment_id          FK Deployment nullable
status                 enum: candidate|dismissed|confirmed
data_confidence        real 0..1
baseline_confidence    real 0..1
change_confidence      real 0..1
correlation_confidence real 0..1
overall_level          enum: high|medium|low|unknown
started_at             timestamp
algorithm_version      string
created_at             timestamp
updated_at             timestamp
```

- `confirmed` exige ação externa explícita; algoritmo só cria `candidate`;
- sem deployment plausível, candidate pode existir com `deployment_id = NULL`.

## 4. Cardinalidades essenciais

```text
Repository 1 ── N Commit
Commit     0..1 ── N Build
Build      0..1 ── N Artifact
Deployment 0..1 ── 1 Artifact
Service    1 ── N RuntimeInstance
Service    1 ── N Endpoint
Service    N ── N Dependency (por observações temporais)
Baseline   1 ── N BehaviorChange
BehaviorChange 1 ── 0..N RegressionCandidate
Correlation 1 ── N Evidence
```

---

# Parte III — Contratos dos casos de uso

## 5. Envelope JSON comum

Sucesso:

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-18T15:00:00Z",
  "command": "runtime",
  "data": {},
  "warnings": [],
  "pagination": null
}
```

Falha:

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-18T15:00:00Z",
  "command": "regression",
  "error": {
    "code": "INSUFFICIENT_DATA",
    "message": "not enough complete windows to build a baseline",
    "retryable": false,
    "details": {"required_windows": 3, "available_windows": 1}
  }
}
```

Error codes públicos:

```text
INVALID_ARGUMENT
INVALID_CONFIG
NOT_FOUND
SOURCE_UNAVAILABLE
INSUFFICIENT_DATA
CONFLICT
ACCESS_DENIED
INCOMPATIBLE_SCHEMA
INTERNAL
```

## 6. `prodmap init`

Entrada:

```text
--project-dir path (default: cwd)
--data-dir path
--force false
--interactive false
```

Validação:

- diretório deve existir e ser gravável;
- `--force` não apaga banco;
- configuração existente só pode ser atualizada preservando campos desconhecidos da mesma schema version.

Saída: paths criados, sources detectadas e próximos passos. Exit `0`, `2`, `3` ou `7`.

## 7. `prodmap doctor`

Entrada: `--source`, `--json`, `--strict`.

Executa checks independentes com timeout. Em modo normal, warnings não falham. Em `--strict`, qualquer check obrigatório diferente de `pass` retorna erro.

```json
{
  "schema_version": "1.0",
  "command": "doctor",
  "data": {
    "status": "warning",
    "checks": [
      {"id":"docker.socket","status":"pass","message":"Docker is reachable"},
      {"id":"otel.source","status":"warning","message":"No telemetry source configured"}
    ]
  },
  "warnings": []
}
```

## 8. `prodmap runtime`

Entrada:

```text
--service string optional
--environment string optional
--at timestamp optional, default now
--refresh false
--limit 100, max 1000
--cursor opaque
```

Sem `--refresh`, consulta o snapshot persistido. Com `--refresh`, sincroniza a source antes da consulta.

Validação: timestamp RFC3339; limit válido; cursor pertence à mesma query.

Saída por item: service, instance, state, health, artifact, commit correlation, freshness e evidências resumidas.

## 9. `prodmap deploys`

Entrada: `--service`, `--environment`, `--since`, `--until`, `--status`, paginação.

Regras:

- intervalo padrão: últimas 24 horas;
- `until > since`;
- ordenação por `started_at DESC, id DESC`;
- deployment sem proveniência completa permanece na saída com confidence `UNKNOWN`.

## 10. `prodmap timeline`

Entrada: filtros de service/environment e intervalo obrigatório ou default de 2 horas.

Normaliza eventos em:

```json
{
  "id": "evt_...",
  "time": "2026-08-18T14:20:00Z",
  "kind": "deployment_succeeded",
  "subject": {"type":"deployment","id":"...","name":"production-182"},
  "source": {"kind":"github","freshness_seconds":42},
  "confidence": {"level":"exact","score":1.0}
}
```

Ordenação determinística por time, prioridade de evento e ID.

## 11. `prodmap graph`

Entrada:

```text
--service required unless --all
--at timestamp default now
--depth integer default 1, max 5
--min-confidence low|medium|high|exact default low
```

Regras:

- grafo é avaliado no instante `--at`;
- ciclos são permitidos, duplicação de nós não;
- limite máximo de nós configurável; truncamento gera warning;
- cada edge inclui tipo, validade, confiança e IDs de evidência.

## 12. `prodmap baseline`

Entrada:

```text
--service ou --endpoint exatamente um
--metric required
--window duration default 30m
--at RFC3339 required
--min-samples integer default 10
--min-coverage float default 0.8
```

Validação:

- Slice 4.1 aceita somente `previous_window`, sem flag de método ou histórico;
- a janela é exatamente `[at-window, at)` e deve existir uma única vez;
- janela entre 5m e 24h; `min-samples` entre 1 e 1.000.000; cobertura entre 0 e 1;
- histórico insuficiente, múltiplas janelas, contaminação por deployment e dados futuros retornam `UNKNOWN`.

Saída: valor, janela usada/rejeitada, cobertura, confidence e justificativas;
nenhum resultado é persistido e `HIGH`/`EXACT` não são permitidos.

## 13. `prodmap regression`

Entrada:

```text
--deployment UUID required
--before duration default 30m
--after duration default 30m
--metric required
--min-samples integer default 10
--min-coverage float default 0.8
```

Validação:

- Slice 4.2/4.3 aceita somente seleção por deployment;
- before e after devem estar entre 5m e 24h;
- para `request_count`, before e after devem ter a mesma duração, pois a unidade
  é `requests` e esta slice não calcula request rate;
- usa somente `[D-before,D)` e `[D,D+after)` exatos para service-level windows;
- dados futuros, janelas ambíguas, insuficientes ou contaminadas retornam `UNKNOWN`;
- `regression-threshold/v1-experimental` classifica sob demanda uma comparação
  válida como `UNKNOWN`, `NO_SIGNAL` ou `CANDIDATE`, com direção `INCREASE`,
  `DECREASE`, `UNCHANGED` ou `UNKNOWN`;
- latência `p50`, `p95` e `p99` requerem inclusivamente `absolute_delta >=
  50000000` ns e `relative_delta >= 0.20`; error rate requer inclusivamente
  `absolute_delta >= 0.05`, permite baseline zero e não requer delta relativo;
- `request_count` permanece não classificável e retorna classificação `UNKNOWN`;
- comparação e classificação têm confidences distintas; ambas são somente `LOW`
  ou `UNKNOWN`, sem causalidade, `HIGH` ou `EXACT`;
- comparação insuficiente resulta em classificação `UNKNOWN`; entrada
  estruturalmente incompatível é rejeitada;
- `classification_key` identifica o algoritmo, versão, thresholds, efeito,
  confidence e `comparison_key`; `generated_at` não participa das chaves;
- não há score, pesos, persistência de comparação/classificação ou migration
  adicional nesta slice.

Saída:

```json
{
  "schema_version": "1.0",
  "command": "regression",
  "data": {
    "comparison_key": "sha256:...",
    "status": "AVAILABLE|UNKNOWN",
    "deployment": {"id": "019...", "environment": "reference", "service": "checkout-api", "started_at": "2026-08-18T14:24:00Z"},
    "metric": "latency_p95",
    "unit": "nanoseconds",
    "before": {"status": "AVAILABLE|UNKNOWN", "accepted_windows": [], "rejected_windows": []},
    "after": {"status": "AVAILABLE|UNKNOWN", "accepted_windows": [], "rejected_windows": []},
    "absolute_delta": 759000000,
    "relative_delta": 4.1703,
    "contamination": {"before_deployments": [], "after_deployments": [], "concurrent_deployments": [], "truncated": false},
    "baseline_confidence": {"level": "LOW|UNKNOWN"},
    "observation_confidence": {"level": "LOW|UNKNOWN"},
    "regression_confidence": {"level": "LOW|UNKNOWN"},
    "classification": {
      "classification_key": "sha256:...",
      "result": "UNKNOWN|NO_SIGNAL|CANDIDATE",
      "direction": "INCREASE|DECREASE|UNCHANGED|UNKNOWN",
      "algorithm": "regression-threshold",
      "algorithm_version": "regression-threshold/v1-experimental",
      "thresholds": {"absolute_min": 50000000, "relative_min": 0.20, "require_all": true, "unit": "nanoseconds"},
      "observed_effect": {"absolute_delta": 759000000, "relative_delta": 4.1703},
      "confidence": {"level": "LOW|UNKNOWN", "basis": "...", "algorithm_version": "regression-threshold/v1-experimental", "limitations": []},
      "causality_claimed": false
    },
    "causality_claimed": false
  },
  "warnings": [],
  "pagination": null
}
```

Os cinco envelopes completos e os casos `CANDIDATE`, `NO_SIGNAL` e `UNKNOWN`
estão em [`phase-4-slice-4.3-json-examples.md`](phase-4-slice-4.3-json-examples.md).

## 14. `prodmap explain`

Entrada: tipo e ID ou seletor inequívoco; `--detail summary|full`; `--at`.

Saída `full` inclui:

- conclusão;
- entidades relacionadas;
- evidence favorável, contraditória e neutra;
- componentes do score e pesos;
- versões de algoritmos;
- freshness das fontes;
- limitações e dados ausentes.

Seletores ambíguos retornam `CONFLICT` com candidatos; nunca escolhem silenciosamente.

## 15. `prodmap evidence`

Recebe correlation ID ou regression candidate ID. Não aceita texto livre. Retorna evidências ordenadas por polaridade e força, com origem e timestamps.

## 16. `prodmap export`

Entrada: investigation/candidate ID, `--output`, `--redaction-profile`, `--include-raw=false`.

Regras:

- arquivo é criado atomicamente;
- overwrite exige `--force`;
- raw permanece desabilitado no MVP;
- pacote inclui manifest, schemas, entidades mínimas, evidências e hashes;
- paths, tokens e PII são redigidos;
- exportação é validada antes de ser entregue.

---

# Parte IV — Algoritmos normativos

## 17. Correlação de proveniência

```text
INPUT: runtime instance R at time T

1. Collect artifact candidates:
   a. exact digest observed in R
   b. immutable image ID
   c. mutable tag (weak evidence only)

2. Match Artifact A:
   if digest matches:
      add identity evidence strength=1.0
   else if only tag matches:
      add identity evidence strength=0.35
   else:
      return UNKNOWN

3. Find deployments D where:
   D.service_key maps to R.service
   D.started_at <= T
   artifact or commit could match

4. For each D:
   provenance =
     1.00 if deployment artifact digest == A.digest
     0.90 if build artifact digest == A.digest
     0.70 if immutable image ID maps uniquely
     0.35 if only mutable tag matches

   temporal = decay(T - D.finished_at)
   topology = service mapping confidence
   exclusivity = 1.0 if no competing successful deployment
                 else proportional penalty

5. Add contradiction evidence for:
   mismatched digest, deployment after observation,
   overlapping candidate, or incompatible environment.

6. Calculate score using versioned weights.

7. EXACT only when immutable identity creates an unambiguous chain.
   Otherwise map numeric score to HIGH/MEDIUM/LOW.

8. Persist correlation, all evidence, weights and algorithm version.
```

Invariantes:

- tag mutável isolada nunca supera LOW;
- timestamp isolado nunca supera LOW;
- dois candidatos indistinguíveis impedem HIGH;
- mismatch de digest impede associação positiva;
- ausência de dado produz UNKNOWN, não evidência negativa.

## 18. Cálculo de confidence

```text
INPUT:
  positive evidence P
  contradictory evidence C
  required dimensions D

1. For each dimension d:
   dimension_score[d] = strongest independent support,
   adjusted to avoid double-counting evidence from same source.

2. weighted_support = Σ weight[d] * dimension_score[d]

3. contradiction_penalty =
   min(1, Σ contradiction_weight[c] * strength[c])

4. completeness = present_required_dimensions / required_dimensions

5. score = clamp(
     weighted_support * completeness * (1 - contradiction_penalty),
     0, 1)

6. Apply hard caps:
   temporal-only       => max 0.39
   mutable-tag-only    => max 0.39
   competing candidate => max 0.59
   stale required data => max configured value

7. Level:
   exact  only by deterministic identity rule
   high   score >= 0.85
   medium score >= 0.60
   low    score > 0
   unknown no minimum evidence
```

Pesos experimentais de referência para `correlation/v0-experimental`:

```text
provenance  0.25
temporal    0.20
topology    0.20
change      0.20
exclusivity 0.15
```

Esses pesos são uma hipótese, não uma metodologia validada nem um contrato permanente. O algoritmo DEVE isolá-los atrás de configuração/versionamento e permitir substituição sem alterar entidades ou contratos públicos. Eles só podem se tornar defaults estáveis após calibração com o corpus de referência. Alterá-los exige benchmark, registro de decisão e nova algorithm version.

As invariantes qualitativas — como impedir `EXACT` estatístico, limitar timestamp ou tag mutável isolados e considerar evidências contraditórias — permanecem normativas mesmo enquanto os pesos forem experimentais.

## 19. Criação de baseline

```text
INPUT: target, metric, event time E, window duration W

1. Candidate previous window:
   [E-W, E)

2. Candidate historical equivalent windows:
   same weekday/time interval in preceding periods,
   using configured timezone only for window selection;
   convert boundaries to UTC.

3. Reject a window if:
   coverage < minimum_coverage
   sample_count < minimum_samples
   it overlaps deployment/rollback/outage exclusion interval
   source reports ingestion gap
   traffic differs beyond configured maximum, when metric requires it

4. For each accepted window calculate metric value.

5. previous method:
   baseline = previous window value

6. historical method:
   baseline = median(values)
   dispersion = MAD(values)

7. combined method:
   if previous and >= minimum historical windows:
      robust weighted combination
   else use available method and cap confidence

8. Confidence components:
   sample adequacy
   coverage
   number of reference windows
   dispersion stability
   traffic comparability
   freshness

9. Persist accepted and rejected window references and reasons.
```

Regras:

- divisão por baseline zero produz delta absoluto e relative delta nulo;
- baseline não usa dados posteriores ao evento;
- janela parcialmente futura é inválida;
- baixa dispersão não compensa baixa cobertura;
- menos de 3 janelas históricas limita confidence a MEDIUM;
- somente uma janela anterior limita confidence a LOW, salvo configuração validada posteriormente.

## 20. Detecção de mudança

```text
INPUT: Baseline B, observed window O

1. Validate matching target, metric and non-overlapping windows.
2. Calculate absolute_delta = O.value - B.value.
3. Calculate relative_delta when B.value != 0.
4. Estimate effect using metric-specific rule.
5. Require both:
   a. minimum absolute or operational threshold
   b. minimum effect threshold
6. Classify direction using metric semantics:
   latency/error increase => degradation
   throughput decrease => degradation only with comparable demand
7. change_confidence = combine effect, samples, coverage and stability.
8. If threshold not met, return no significant change.
9. Persist BehaviorChange only for meaningful changes.
```

## 21. Detecção de regressão candidata

```text
INPUT: deployment D, before Wb, after Wa

1. Resolve deployed service and artifact provenance.
2. Build baseline ending no later than D.started_at.
3. Build observed windows after deployment stabilization delay.
4. Detect meaningful behavior changes.
5. For each degradation:
   a. temporal score from delay after D
   b. topology score from affected target to deployed service
   c. change score from BehaviorChange
   d. provenance score from deployment chain
   e. exclusivity score after searching concurrent events
6. Add contradictions:
   change began before D
   another deployment is closer
   affected service is disconnected
   telemetry gap overlaps boundary
   traffic regime changed materially
7. Calculate four separate confidences:
   data, baseline, change, correlation
8. overall_level is limited by the weakest mandatory component:
   HIGH requires all mandatory scores >= configured HIGH floor
   UNKNOWN if any mandatory component lacks minimum data
9. Create status=candidate only.
10. Present "associated with", never "caused by".
```

## 22. Detecção de eventos concorrentes

Pesquisar no intervalo configurável ao redor da mudança:

- deployments do mesmo serviço;
- deployments de dependências observadas;
- rollback;
- restart storm;
- health check failure;
- gap ou mudança de source;
- alteração material de tráfego.

Cada evento concorrente gera evidência. O algoritmo NÃO elimina automaticamente o candidato; reduz exclusivity e explica a ambiguidade.

---

# Parte V — Invariantes e critérios de aceite

## 23. Invariantes obrigatórias

1. Relação `EXACT` possui identidade determinística verificável.
2. Relação estatística nunca é `EXACT`.
3. Ausência de dados é `UNKNOWN`, nunca `LOW`.
4. Runtime não aponta para dois artifacts no mesmo intervalo válido.
5. Intervalos usam limites `[start, end)` e não se sobrepõem para a mesma identidade.
6. Tag mutável não prova identidade de artefato.
7. Regressão criada pelo sistema sempre começa como `candidate`.
8. Toda conclusão possui versão do algoritmo.
9. Toda confidence possui score, level e evidências recuperáveis.
10. Baseline não contém dados posteriores ao evento analisado.
11. Outputs JSON permanecem válidos mesmo quando warnings são emitidos.
12. Dados sensíveis não aparecem em log, JSON ou export por padrão.
13. Reingestão do mesmo lote não cria duplicatas.
14. Ordem de chegada não altera o resultado final para o mesmo conjunto de fatos.
15. Seleção ambígua falha explicitamente.

## 24. Definition of Done por caso de uso

Uma implementação só está concluída quando:

- contrato de entrada e saída está implementado;
- validações e todos os error codes relevantes têm testes;
- saída humana e JSON possuem golden tests;
- operação respeita cancelamento e timeout;
- persistência da capacidade é transacional e idempotente;
- dados sensíveis passam por testes de redaction;
- race detector passa;
- casos de dados ausentes, duplicados, atrasados e contraditórios são cobertos;
- documentação do comando contém pelo menos um exemplo executável;
- alteração de contrato atualiza schema e changelog.

## 25. Fixtures canônicas

O repositório DEVE conter cenários versionados:

```text
testdata/scenarios/
  exact_provenance/
  mutable_tag_ambiguous/
  deployment_without_artifact/
  regression_high_confidence/
  regression_low_baseline_confidence/
  concurrent_deployments/
  traffic_shift_false_positive/
  telemetry_gap/
  out_of_order_ingestion/
  sensitive_attributes/
```

Cada cenário contém:

- inputs das sources;
- relógio fixo;
- configuração;
- estado esperado do banco;
- JSON esperado por comando;
- confidences e evidências esperadas;
- explicação da razão do resultado.

## 26. Decisões ainda abertas

Estas decisões exigem experimento ou ADR adicional antes da fase correspondente:

- driver SQLite e biblioteca de migração;
- framework CLI, se algum;
- tamanho padrão das janelas e thresholds por métrica;
- função de decaimento temporal;
- extensões futuras da normalização de nomes de serviços além da regra fechada da Phase 2A;
- estratégia de compactação e retenção;
- formato do investigation package;
- transporte do servidor MCP;
- política de compatibilidade entre minors.

“Aberta” não autoriza implementação arbitrária. A issue responsável deve resolver a decisão e registrar o ADR antes de introduzir dependência ou contrato duradouro.

---

## 27. Ordem recomendada de implementação

```text
0. preparar e executar a Phase -1 e registrar go, pivot ou stop; por autorização explícita, a Foundation, o protótipo mínimo da Phase 1, a Phase 2A e a conclusão limitada da Phase 2 usada para produzir e aprender com o pacote experimental podem anteceder a decisão sem representar `go`; a continuação é `continue-for-learning`, termina no gate da Phase 2 e mantinha a Phase 3 bloqueada antes da autorização separada da Decision 002
1. materializar somente os ADRs necessários para Phase 0/1
2. criar o esqueleto mínimo por capacidades
3. implementar tipos fundamentais: IDs, tempo, enums e erros
4. criar a menor migration exigida pela vertical slice
5. implementar Git → Commit
6. implementar Docker → container + image digest
7. implementar Artifact e a correlação por metadata OCI/proveniência
8. implementar Evidence, Confidence e Explain
9. cobrir EXACT, LOW/ambíguo e UNKNOWN com fixtures
10. entregar init, doctor, runtime e explain
11. avaliar a arquitetura com o código e uso reais
12. executar a Phase 2A offline e concluir a Production Graph somente sob `continue-for-learning`, com a aplicação de referência; revisar explicitamente a tese no gate da Phase 2 antes de qualquer avanço para a Phase 3
13. somente após nova decisão explícita que autorize a Phase 3, implementar DeploymentSource e GitHub
14. implementar Baseline, BehaviorChange e RegressionCandidate
15. validar com corpus de falsos positivos
16. implementar export e, somente depois, MCP
```

Cada item deve ser decomposto em issues pequenas com objetivo, arquivos previstos, critérios de aceite, fixtures e fora de escopo.

### 27.1 Vertical slice obrigatória da Phase 1

```text
Git
  ↓
Commit
  ↓
Artifact
  ↓
Docker runtime
  ↓
Evidence
  ↓
Correlation
  ↓
Explain
```

O primeiro fluxo deve provar `container → image digest → artifact → commit` sem assumir que a relação sempre existe.

Fixtures mínimas:

```text
digest + revision OCI verificável → EXACT
apenas tag mutável                → LOW
sem metadata de commit            → UNKNOWN
```

Somente após essa slice funcionar ponta a ponta será permitido expandir o schema. Sob `continue-for-learning`, a Phase 2 pode incluir OTel offline, grafo observado, agregados temporais limitados, integração entre runtime inventory e serviços observados somente mediante evidência, consultas temporais, controle de cardinalidade e validação com a aplicação de referência. Antes da [Decision 002](decisions/002-phase-3-limited-learning.md), deployments, baseline, regression, métricas e logs OTLP, receiver vivo, MCP e a Phase 3 permaneciam bloqueados. A tese deve ser revisada no gate da Phase 2; a Decision 002 autoriza separadamente e somente a Phase 3 como aprendizado limitado, mantendo todos os demais itens bloqueados.

O [Phase 2 Gate Review](reviews/phase-2-gate-review.md) registra o resultado
técnico desse gate sem, por si, o converter em validação da tese ou autorização
da Phase 3.

---

## 28. Regra final

Quando houver dúvida entre produzir uma conclusão conveniente e admitir incerteza, a implementação DEVE admitir incerteza.

O valor do Prodmap não depende de sempre encontrar uma regressão. Depende de tornar explícito o que é fato, o que é inferência, quais dados faltam e por que uma conclusão merece — ou não merece — confiança.
