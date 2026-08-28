# Prodmap — Product Specification v2

> **Production context for coding agents.**  
> Conecte mudanças de código ao comportamento real em produção.

**Status:** proposta para validação  
**Implementação principal:** Go  
**Licença pretendida:** open source  
**Interface inicial:** CLI; API e MCP em fases posteriores

**Especificação técnica complementar:** [`prodmap-technical-spec-v1.md`](./prodmap-technical-spec-v1.md)

---

## 1. Resumo executivo

Prodmap é uma ferramenta DevOps local-first e vendor-neutral que constrói contexto operacional a partir de dados já existentes em Git, pipelines de deployment, runtimes e sistemas de observabilidade.

Seu objetivo é responder, com evidências e níveis explícitos de confiança:

> **O que está rodando em produção, o que mudou e qual é a relação provável entre essa mudança e o comportamento observado?**

O Prodmap não substitui GitHub, Docker, OpenTelemetry, Prometheus, Grafana, Sentry ou Datadog. Ele conecta fatos produzidos por essas ferramentas em um modelo comum, preservando a origem e a força de cada relação.

Exemplo:

```text
commit a81f23
  ↓ EXACT — SHA informado pelo deployment
deployment production-182
  ↓ EXACT — digest do artefato
image sha256:9ac...
  ↓ EXACT — imagem observada no container
checkout-api
  ↓ OBSERVED — spans do serviço
POST /checkout
  ↓ STATISTICAL — mudança após o deployment
p95 182 ms → 941 ms
```

O resultado deve ser útil diretamente para pessoas e, posteriormente, para agentes de programação por meio de MCP. O Prodmap fornece fatos, relações e evidências; o agente fornece raciocínio e propõe alterações no código.

---

## 2. Hipótese central e principal risco

A hipótese do produto é:

> Contexto de produção previamente correlacionado permite que pessoas e agentes diagnostiquem problemas com mais rapidez, precisão e menos exploração do que o acesso bruto e separado a logs, métricas e traces.

O principal risco é de tese, não de implementação. Um agente competente, com acesso direto às fontes, talvez produza um diagnóstico equivalente sem precisar do Prodmap. Por isso, a validação dessa hipótese é a primeira fase do projeto e uma condição para investir no roadmap completo. Antes da autorização posterior da Phase 3, somente a Foundation, os protótipos explicitamente autorizados para o experimento e a conclusão limitada da Phase 2 autorizada pela [Decision 001](decisions/001-directional-pilot-continuation.md) PODIAM avançar como investimento de validação ou aprendizado de produto. Isso não constituiu `go` nem validou a tese. A [Decision 002](decisions/002-phase-3-limited-learning.md) autoriza separadamente e somente a Phase 3 como aprendizado limitado.

### 2.1 Experimento 001 — correlação versus contexto bruto

Usar um projeto real com Git, histórico de deployments e OpenTelemetry. Preparar incidentes conhecidos ou cenários reproduzíveis, como uma regressão de latência no checkout.

Comparar dois grupos sob as mesmas condições:

- **Controle:** agente com acesso bruto às fontes de telemetria e deployment.
- **Prodmap:** agente com o mesmo acesso, acrescido de um pacote de contexto correlacionado equivalente ao futuro `prodmap regression`.

Medir:

- tempo até o primeiro diagnóstico correto;
- número de consultas e volume de dados recuperado;
- precisão da causa provável;
- qualidade e rastreabilidade das evidências;
- quantidade de hipóteses incorretas;
- capacidade de reproduzir o diagnóstico;
- avaliação cega da resposta por um desenvolvedor.

### 2.2 Critério de continuidade

Antes do experimento, devem ser definidos limiares objetivos. Como ponto de partida, a construção do produto completo só deve continuar se o contexto correlacionado demonstrar ganho consistente em pelo menos dois eixos importantes — por exemplo, redução relevante no tempo/consultas e melhoria na precisão ou na explicabilidade — sem piora material nos demais.

Se a diferença não for clara, o projeto deve revisar a proposta de valor. Alternativas incluem focar em inventário de runtime, proveniência de artefatos, formato de contexto padronizado ou investigação reprodutível, em vez de afirmar superioridade diagnóstica.

---

## 3. Problema

Agentes de programação normalmente conhecem bem:

- código-fonte e estrutura do repositório;
- histórico Git;
- testes, documentação e issues;
- dependências declaradas.

Entretanto, geralmente não sabem com segurança:

- qual versão está realmente em execução;
- qual commit originou cada artefato;
- quando e como ocorreu um deployment;
- quais serviços, endpoints e dependências são observados em produção;
- como o comportamento mudou depois de uma alteração;
- se uma coincidência temporal é evidência forte ou fraca;
- se a baseline usada numa comparação é confiável;
- quais fontes sustentam uma conclusão.

Dar acesso a `get_logs`, `get_metrics` e `get_traces` transfere para o agente o trabalho de descobrir essas relações, repete investigação e amplia o risco de conclusões baseadas apenas em proximidade temporal.

---

## 4. Proposta de valor

O Prodmap transforma dados fragmentados em contexto operacional estruturado, explicável e reutilizável.

Ele deve responder perguntas como:

- Qual commit e qual imagem estão em produção?
- O runtime corresponde ao último deployment registrado?
- O último deployment precedeu alguma mudança relevante de comportamento?
- Quais endpoints e dependências foram afetados?
- A baseline é suficientemente confiável para chamar isso de regressão?
- Há outro deployment ou evento concorrente que enfraqueça a hipótese?
- Quais evidências sustentam cada ligação?
- O que mudou no código relacionado à área afetada?

### 4.1 Resultado esperado

```text
Regression candidate

Service: checkout-api
Endpoint: POST /checkout
Deployment: production-182
Commit: a81f23

p95:        182 ms → 941 ms (+417%)
error rate: 0.17%  → 4.72%
started:    4m after deployment

Main affected dependency: postgres

Baseline confidence: MEDIUM (0.74)
Correlation confidence: HIGH (0.89)

Evidence:
✓ deployed artifact contains commit a81f23
✓ running image digest matches deployment artifact
✓ affected service matches deployment target
✓ change started 4m after deployment
✓ endpoint traces contain increased database duration
~ historical comparison is available for only 3 equivalent windows
✓ no concurrent deployment was detected

This is a correlation, not proof of causality.
```

---

## 5. Público-alvo e adoção progressiva

### 5.1 Persona principal

Desenvolvedor individual ou pequena equipe que opera aplicações em Docker, VPS ou infraestrutura equivalente e não possui uma plataforma DevOps interna. Pode usar coding agents, mas não precisa ter observabilidade madura.

### 5.2 Persona secundária

Backend engineers, DevOps e SREs que desejam:

- rastrear proveniência de versões;
- correlacionar deployments e comportamento;
- oferecer contexto operacional seguro a agentes;
- investigar incidentes sem navegar manualmente entre diversas ferramentas.

### 5.3 Escada de valor

O produto não deve exigir GitHub Actions, Docker e OpenTelemetry simultaneamente para ser útil.

1. **Git + runtime:** inventário de versão, commit, imagem, container, uptime, saúde e reinícios.
2. **Fonte de deployment:** timeline e proveniência do artefato.
3. **Traces/métricas:** grafo observado e mudanças de comportamento.
4. **Histórico suficiente:** regressões com baseline e confiança.
5. **MCP:** contexto estruturado para agentes.

Um starter opcional com OTel Collector em Docker Compose deve reduzir a barreira de instrumentação, mas não será pré-requisito para os primeiros resultados.

---

## 6. Posicionamento e diferenciais

Prodmap não é:

- coletor ou armazenamento geral de logs;
- dashboard de observabilidade;
- substituto para tracing, métricas ou error tracking;
- sistema de CI/CD;
- agente de IA ou mecanismo autônomo de correção;
- ferramenta que prova causalidade apenas por proximidade temporal.

### 6.1 Diferencial proposto

> **Uma camada local-first e vendor-neutral que cria contexto correlacional explicável e reutilizável por humanos e agentes a partir de múltiplas fontes.**

Os diferenciais que precisam ser demonstrados, e não apenas declarados, são:

- modelo comum para código, deployment, artefato, runtime e telemetria;
- relações acompanhadas de evidências e confiança;
- distinção entre confiança da correlação e qualidade da baseline;
- funcionamento útil antes da adoção completa de observabilidade;
- processamento local e controle explícito sobre dados sensíveis;
- resultados determinísticos e consumíveis por qualquer agente;
- arquitetura extensível por fontes, sem vincular o domínio a um fornecedor.

### 6.2 Concorrência e fronteira do produto

Soluções como Sentry Release Tracking e Datadog Deployment Tracking já associam releases ou deployments ao comportamento observado. Plataformas de incident management também agregam sinais de diversas fontes e vêm incorporando agentes e integrações MCP.

O Prodmap só merece existir se entregar uma combinação que essas alternativas não ofereçam adequadamente ao público-alvo: instalação simples, independência de fornecedor, operação local, modelo explícito de proveniência, confiança explicável e contexto portátil entre pessoas e agentes. A análise competitiva deve ser revisada durante a Phase -1 e a cada release relevante.

---

## 7. Princípios de produto

1. **Validar antes de escalar.** A tese precede a infraestrutura completa.
2. **Valor progressivo.** Cada nova fonte melhora o resultado, mas o produto começa útil com poucas fontes.
3. **Evidência antes de conclusão.** Toda correlação informa por que existe.
4. **Correlação não é causalidade.** A linguagem da interface deve preservar essa distinção.
5. **Facts over reasoning.** O núcleo não depende de LLM.
6. **Local-first e seguro por padrão.** Dados não deixam o ambiente sem ação explícita.
7. **Vendor-neutral.** Integrações são adaptadores, não o domínio.
8. **Determinístico e reprodutível.** A mesma entrada e configuração produzem a mesma análise.
9. **Degradação honesta.** Dados ausentes reduzem confiança; não produzem falsa precisão.
10. **CLI primeiro.** A interface web só será considerada depois de validar o núcleo.

---

## 8. Modelo de domínio — Production Graph

### 8.1 Entidades iniciais

- `Repository`
- `Commit`
- `Build`
- `Artifact`
- `Deployment`
- `RuntimeInstance`
- `Service`
- `Endpoint`
- `Dependency`
- `TelemetryWindow`
- `BehaviorChange`
- `RegressionCandidate`
- `Incident`
- `Evidence`

### 8.2 Relações

```text
Repository → contains → Commit
Commit → produced_by/builds → Artifact
Deployment → deploys → Artifact
RuntimeInstance → runs → Artifact
RuntimeInstance → belongs_to → Service
Service → exposes → Endpoint
Service → calls → Dependency
TelemetryWindow → describes → Service/Endpoint/Dependency
BehaviorChange → observed_in → TelemetryWindow
RegressionCandidate → associated_with → Deployment
Evidence → supports/contradicts → Relation or Conclusion
```

### 8.3 Temporalidade e proveniência

Entidades e relações devem registrar:

- `observed_at`, `valid_from` e, quando aplicável, `valid_to`;
- fonte e identificador original;
- horário de ingestão;
- método de descoberta;
- versão do schema e do algoritmo;
- evidências favoráveis e contraditórias.

O grafo é histórico. Atualizar um deployment ou runtime não deve apagar o estado anterior necessário para reproduzir uma investigação.

---

## 9. Modelo de correlação e confiança

### 9.1 Tipos de relação

- **DECLARED:** informado por configuração ou usuário.
- **EXACT:** identidade verificável, como SHA ou digest coincidente.
- **OBSERVED:** derivado de observação direta, como spans entre serviços.
- **INFERRED:** inferência apoiada por múltiplos sinais.
- **STATISTICAL:** associação derivada de comparação de séries temporais.

O tipo descreve a natureza da relação; o nível de confiança descreve a força das evidências. Eles não devem ser confundidos.

### 9.2 Níveis de confiança

- **EXACT:** identidade verificável sem ambiguidade relevante.
- **HIGH:** múltiplas evidências independentes e nenhuma contradição importante.
- **MEDIUM:** evidência útil, porém incompleta ou com alternativas plausíveis.
- **LOW:** associação fraca, normalmente temporal ou baseada em poucos dados.
- **UNKNOWN:** dados insuficientes para uma avaliação responsável.

### 9.3 Scoring inicial

Uma correlação pode combinar componentes normalizados entre 0 e 1:

```text
correlation_score =
  provenance_weight * provenance_score +
  temporal_weight   * temporal_score +
  topology_weight   * topology_score +
  change_weight     * change_score +
  exclusivity_weight * exclusivity_score
```

Os pesos, limiares e versões devem ser configuráveis, testados e exibidos no modo detalhado. Evidências contraditórias aplicam penalidades explícitas. O score nunca substitui a lista de evidências.

Mapeamento inicial sugerido, sujeito à validação:

```text
EXACT   identidade determinística verificada
HIGH    score ≥ 0.85
MEDIUM  score ≥ 0.60
LOW     score > 0 e < 0.60
UNKNOWN sem dados mínimos
```

### 9.4 Confianças separadas

Uma saída de regressão deve distinguir pelo menos:

- **Data confidence:** completude e qualidade das fontes.
- **Baseline confidence:** adequação da referência estatística.
- **Change confidence:** força da mudança observada.
- **Correlation confidence:** força da associação com o deployment.

Uma regressão com mudança clara, mas baseline fraca, não pode ser apresentada como fato de alta confiança.

---

## 10. Detecção confiável de regressões

### 10.1 Requisitos mínimos

O primeiro release não deve depender apenas de `30 minutos antes versus 30 minutos depois`. Deve:

- exigir amostras mínimas configuráveis;
- comparar volume/tráfego, não apenas percentuais;
- usar janela imediatamente anterior;
- usar, quando disponível, uma ou mais janelas históricas equivalentes;
- detectar dados ausentes e gaps de ingestão;
- identificar deployments e eventos concorrentes;
- informar tamanho do efeito e não só significância;
- calcular confiança da baseline separadamente;
- evitar uma conclusão forte quando o histórico for insuficiente.

### 10.2 Métricas iniciais

- request count;
- error count e error rate;
- latência p50, p95 e p99;
- duração de spans por dependência;
- taxa de reinícios e falhas de health check, quando disponíveis.

### 10.3 Saída

A saída pode ser `no significant change`, `change detected` ou `regression candidate`. O termo `regression confirmed` não deve ser usado sem um critério externo de confirmação.

### 10.4 Evolução

Após validar a abordagem: ajuste sazonal mais sofisticado, segmentação por tráfego, comparação por cohort, change-point detection e calibração baseada em falsos positivos/negativos rotulados.

---

## 11. Arquitetura de fontes

Fontes devem implementar contratos orientados ao domínio. GitHub Actions será uma fonte de deployment, não a definição de deployment.

```go
type DeploymentSource interface {
	Name() string
	SyncDeployments(ctx context.Context, since time.Time) ([]Deployment, error)
}

type RuntimeSource interface {
	Name() string
	InspectRuntime(ctx context.Context) ([]RuntimeInstance, error)
}

type TelemetrySource interface {
	Name() string
	Query(ctx context.Context, query TelemetryQuery) (TelemetryResult, error)
}
```

Fontes planejadas:

- **Inicial:** Git local, Docker Engine, OpenTelemetry.
- **Próxima:** GitHub/GitHub Actions.
- **Futura:** GitLab CI, Kubernetes, Prometheus e fornecedores de observabilidade.
- **Possível:** MiniPaaS como produtor nativo de eventos operacionais.

### 11.1 Integração futura com MiniPaaS

Os projetos permanecem independentes. O MiniPaaS poderá publicar eventos como:

```text
DeploymentStarted
DeploymentSucceeded
DeploymentFailed
RollbackStarted
RollbackSucceeded
ContainerRestarted
HealthCheckFailed
```

O Prodmap ingere esses eventos pelo mesmo contrato usado por outras fontes. Nenhuma entidade central deve conter lógica específica do MiniPaaS.

---

## 12. Requisitos funcionais

### RF-001 — Inicialização

`prodmap init` cria configuração local, identifica repositório e prepara o armazenamento sem enviar dados externamente.

### RF-002 — Diagnóstico de configuração

`prodmap doctor` verifica permissões, conectividade, relógio, fontes disponíveis, qualidade dos metadados e ações recomendadas.

### RF-003 — Inventário de runtime

O sistema lista serviços e instâncias, imagem/digest, labels, status, uptime, health e restart count quando disponíveis.

### RF-004 — Mapeamento de proveniência

O sistema tenta ligar runtime → artefato → deployment → commit, mantendo evidências e confiança por relação.

### RF-005 — Timeline

O sistema apresenta eventos ordenados de commit, build, deployment, rollback, restart, falha de health check e mudanças observadas.

### RF-006 — Grafo de produção

O sistema constrói e consulta relações observadas entre serviços, endpoints e dependências, preservando intervalo temporal.

### RF-007 — Baselines

O sistema calcula e persiste baselines com método, amostras, janelas, cobertura e confiança.

### RF-008 — Regressões candidatas

`prodmap regression` identifica mudanças relevantes, associa deployments quando justificado e exibe evidências, contradições e confianças separadas.

### RF-009 — Explicação

`prodmap explain <service|endpoint|deployment>` produz uma visão detalhada, legível e rastreável da conclusão.

### RF-010 — Saída estruturada

Comandos relevantes aceitam `--json`, com schema versionado e estável o suficiente para automação.

### RF-011 — MCP

Depois de validar CLI e domínio, o sistema oferece tools de leitura com respostas compactas, schemas versionados e links para evidências.

### RF-012 — Exportação e reprodução

O sistema exporta um pacote sanitizado de investigação que permite reproduzir uma conclusão sem acessar novamente todas as fontes.

### RF-013 — Retenção e limpeza

O usuário configura retenção por categoria e pode inspecionar e remover dados locais de forma previsível.

---

## 13. CLI proposta

```bash
prodmap init
prodmap doctor
prodmap status
prodmap services
prodmap runtime
prodmap deploys
prodmap timeline
prodmap graph
prodmap baseline
prodmap regression
prodmap explain <target>
prodmap evidence <correlation-id>
prodmap export <investigation-id>
prodmap mcp serve
```

Exemplos:

```bash
prodmap runtime --service checkout-api
prodmap timeline --since 2h
prodmap regression --deployment production-182
prodmap explain POST:/checkout --json
```

---

## 14. MCP proposto

O MCP não deve apenas espelhar consultas brutas de observabilidade. Deve expor contexto já estruturado:

```text
get_runtime_status
get_deployment
get_production_timeline
get_service_context
get_endpoint_context
get_dependency_graph
get_recent_behavior_changes
get_regression_candidate
get_correlation_evidence
get_production_context
```

Requisitos:

- leitura por padrão;
- escopo e limites configuráveis;
- respostas compactas e paginadas;
- timestamps, fonte, confiança e schema em cada resposta;
- redaction antes da exposição;
- nenhuma alteração de produção no MVP;
- proteção contra conteúdo não confiável oriundo de logs e atributos.

---

## 15. Requisitos não funcionais

### 15.1 Segurança e privacidade

- nenhum upload automático;
- segredos nunca são persistidos em texto puro;
- filtros para headers, tokens, parâmetros, payloads e atributos sensíveis;
- princípio do menor privilégio para cada source;
- logs internos sem credenciais;
- documentação de threat model antes do MCP;
- trilha de auditoria para consultas e exportações;
- limites de tamanho e tempo para dados externos.

### 15.2 Confiabilidade

- ingestão idempotente;
- tolerância a reprocessamento e eventos fora de ordem;
- transações para atualizações correlatas;
- migrações versionadas e testadas;
- recuperação previsível após interrupção;
- relógios e fusos normalizados em UTC internamente.

### 15.3 Desempenho

Metas iniciais, a validar em benchmarks:

- comandos de inventário local respondem em até 2 s em ambiente pequeno aquecido;
- consultas comuns respondem em até 3 s no percentil 95 para o dataset de referência;
- ingestão é incremental e limitada por configuração;
- uso de memória permanece previsível sob cardinalidade adversa.

### 15.4 Portabilidade

- Linux e macOS no MVP;
- binário único por plataforma;
- dependências externas opcionais e documentadas;
- armazenamento padrão em SQLite, abstraído onde houver benefício comprovado.

### 15.5 Compatibilidade

- schemas de saída possuem versão;
- breaking changes exigem migração e changelog;
- integrações equivalentes devem passar por contract tests comuns.

### 15.6 Observabilidade do próprio Prodmap

- logs estruturados;
- níveis de log configuráveis;
- métricas internas opcionais;
- diagnósticos de duração e falha por source;
- correlation IDs nas operações internas.

---

## 16. Engenharia e melhores práticas de Go

O projeto deve seguir práticas idiomáticas de Go desde a base:

- versões suportadas do Go explicitadas em `go.mod` e na política de suporte;
- organização por domínio e responsabilidade, evitando abstrações prematuras;
- pacotes pequenos, coesos e com APIs mínimas;
- interfaces definidas próximas de quem as consome;
- `context.Context` propagado em operações de I/O e cancelamento;
- erros envolvidos com contexto por `%w` e examinados com `errors.Is/As`;
- nenhuma dependência de `panic` para fluxo normal;
- concorrência limitada, cancelável e sem goroutines órfãs;
- fechamento explícito de recursos;
- configuração validada no startup;
- timestamps em UTC e unidades explícitas;
- injeção de relógio onde o tempo afetar testes;
- sem estado global mutável desnecessário;
- comentários e documentação para APIs públicas;
- dependências externas reduzidas e justificadas.

### 16.1 Qualidade obrigatória

```text
gofmt
go vet
go test ./...
go test -race ./...
static analysis/lint
dependency and vulnerability scanning
```

Além disso:

- testes unitários para regras de domínio e scoring;
- testes de tabela para casos e limites;
- golden tests para saídas CLI/JSON quando apropriado;
- contract tests compartilhados entre sources;
- integration tests com Docker e OTel Collector;
- fuzz tests para parsers, ingestão e dados externos;
- benchmarks para correlação, consultas e cardinalidade;
- cobertura de falhas, timeouts, duplicação e eventos fora de ordem;
- CI em plataformas suportadas;
- releases reproduzíveis, checksums e artefatos assinados quando viável.

### 16.2 Critérios de revisão

Cada mudança deve preservar:

- clareza do domínio;
- compatibilidade de schema ou migração explícita;
- testes proporcionais ao risco;
- ausência de vazamento de dados sensíveis;
- limites de recursos e cancelamento;
- explicabilidade das conclusões.

---

## 17. Arquitetura técnica inicial

O Prodmap usa um monólito modular organizado por capacidades, não uma sequência rígida de camadas horizontais. Tipos, regras e interfaces ficam próximos da capacidade responsável; integrações concretas permanecem fora dessas capacidades.

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

# adicionados quando a capacidade existir:
  deployment/
  telemetry/
  regression/
  otel/
  github/
```

Regras:

- não existe package global obrigatório `domain`, `ports`, `adapters`, `repositories` ou `storage`;
- interfaces são pequenas, definidas pelo package consumidor e criadas somente quando um caso de uso precisar delas;
- integrações como `git`, `docker`, `otel`, `github` e `sqlite` convertem dados externos para contratos das capacidades;
- SDKs externos e modelos de persistência não entram nos tipos das capacidades;
- modelo de domínio, row de banco e DTO JSON não precisam — e normalmente não devem — ser a mesma struct;
- packages e tabelas futuras não são criados antecipadamente;
- abstrações de persistência expressam operações do caso de uso, não CRUD universal.

As regras de correlação e confiança devem permanecer testáveis sem rede, Docker ou banco real.

---

## 18. Roadmap

### Phase -1 — Thesis Validation

**Objetivo:** provar que correlação estruturada agrega valor sobre contexto bruto.

Entregas:

- dataset/cenários de referência;
- protótipo manual do pacote de contexto;
- protocolo e critérios do Experimento 001;
- comparação documentada e decisão go/pivot/stop;
- análise atualizada de concorrência;
- rascunho da Correlation Model Specification.

**Gate:** ganho consistente e mensurável, ou uma tese revisada que justifique continuar.

**Exceção de validação:** Phase 0, o menor protótipo da Phase 1, a Phase 2A e a conclusão limitada da Phase 2 puderam avançar sob [`continue-for-learning`](decisions/001-directional-pilot-continuation.md): um investimento interno limitado, distinto de `go`, `pivot` e `stop`. Essa autorização não validou a tese, terminou no gate da Phase 2 e exigiu uso da aplicação de referência e nova revisão explícita da tese. A [Decision 002](decisions/002-phase-3-limited-learning.md) autorizou depois, e somente, a Phase 3 como aprendizado limitado.

### Phase 0 — Foundation

**Objetivo:** criar uma base pequena e confiável em Go.

Entregas:

- CLI, configuração e logging;
- ambiente de desenvolvimento com hot reload versionado e um comando único (`make dev`);
- somente os tipos exigidos pela primeira vertical slice;
- SQLite e a menor migration necessária;
- schemas JSON versionados;
- CI, testes, lint, race detector e security scanning;
- `prodmap init` e `prodmap doctor`.

**Gate:** instalação reproduzível, hot reload validado, migrações seguras e pipeline verde.

### Phase 1 — Runtime Provenance Vertical Slice

**Objetivo:** entregar valor com Git + Docker, sem OTel, provando um fluxo ponta a ponta antes de expandir o schema.

Fluxo obrigatório:

```text
Git → Commit → Artifact → Docker runtime → Evidence → Correlation → Explain
```

Entregas:

- descoberta de containers e serviços;
- imagem, digest, labels, health, uptime e restart count;
- ligação `container → image digest → artifact → commit` quando houver evidência;
- `status`, `services`, `runtime` e `explain` básico;
- evidências e confiança por relação.

Fixtures obrigatórias:

```text
digest + revision OCI verificável → EXACT
apenas tag mutável                → LOW
sem metadata de commit            → UNKNOWN
```

**Gate:** executar a vertical slice ponta a ponta, identificar de forma confiável o que está rodando e admitir `UNKNOWN` quando a proveniência não puder ser determinada. Entidades de OTel, graph, baseline e regression não devem virar tabelas antes desse gate.

### Phase 2A — OTel Validation Slice (exceção experimental)

**Objetivo:** produzir somente o componente topológico reprodutível necessário ao pacote correlacionado do Experimento 001.

Entregas limitadas:

- ingestão offline de traces OTLP JSONL congelados;
- serviços, endpoints, dependências `OBSERVED` e agregados temporais limitados;
- persistência local e consulta `graph`;
- starter opcional e versionado do OTel Collector.

Métricas e logs OTLP, receiver vivo, deployments, baseline, regression, export genérico e MCP permanecem fora. A conclusão da slice não constitui `go`; a conclusão limitada da Phase 2 somente é permitida sob `continue-for-learning` e termina no seu gate.

### Phase 2 — Production Graph

**Objetivo:** mapear comportamento observado.

Entregas:

- ingestão OpenTelemetry;
- starter opcional do OTel Collector via Docker Compose;
- serviços, endpoints e dependências observadas;
- temporalidade do grafo;
- `graph` e contexto de serviço/endpoint.
- contexto temporal de endpoint por `prodmap endpoints`, preservando janelas observadas sem agregação sobreposta.

**Gate:** grafo reprodutível em aplicações de referência, com controle de cardinalidade.

**Autorização limitada:** a Phase 2 pode ser concluída sob `continue-for-learning`, com a aplicação de referência e cenários mais representativos. A tese deve receber nova revisão explícita neste gate; a autorização não é `go` e não desbloqueia, por si, a Phase 3.

O [Phase 2 Gate Review](reviews/phase-2-gate-review.md) registra o PASS técnico
sem validar a tese. No instante daquele gate, a decisão formal da Phase -1
permanecia pendente e a Phase 3 estava bloqueada até decisão explícita do owner.

### Phase 3 — Deployment Intelligence

**Objetivo:** criar timeline e proveniência de deployments.

**Status atual:** **AUTHORIZED FOR LIMITED LEARNING** pela
[Decision 002](decisions/002-phase-3-limited-learning.md). Esta autorização não
é `go`, não valida a tese e permite implementação em slices verticais pequenas e
revisáveis. No momento da Decision 002, a Phase 4 permanecia bloqueada até nova
decisão explícita.

**Slice 3.1 materializada:** ledger offline versionado, ingestão SQLite atômica e
idempotente e consulta `deploys`; não inclui Build, timeline, fonte remota ou
correlação deployment/runtime.

**Slice 3.2 materializada:** `deploys` deriva associação deployment/runtime por
identidade imutável, environment, serviço e janela temporal; a relação é
inferida, não causal e não é persistida.

**Slice 3.3 materializada:** `timeline` une dinamicamente eventos declarados de
deployment e observações runtime, preservando concorrência e rollback declarado
sem afirmar efeito de runtime ou causalidade.

**Slices 3.4A, 3.4B1 e 3.4B2 materializadas:** fronteira segura e testável para
artifact GitHub Actions que transporta o contrato `deployment-ledger-jsonl/v1`,
CLI/persistência atômica do ledger e workflow controlado para publicação e sync.
O [Phase 3 Technical Gate Review](reviews/phase-3-gate-review.md) registra o
PASS técnico limitado às evidências revisadas; não é `go` nem valida a tese. A
[Decision 003](decisions/003-phase-4-limited-learning.md) autoriza depois a
Phase 4 somente como aprendizado limitado; Phase 5 permanece bloqueada.

Entregas:

- contrato `DeploymentSource`;
- integração GitHub/GitHub Actions;
- commit → build/artefato → deployment → runtime;
- eventos de rollback e deployment concorrente;
- `deploys` e `timeline`.

**Gate:** proveniência explicável ponta a ponta no cenário suportado.

### Phase 4 — Reliable Regression Detection

**Objetivo:** detectar mudanças sem sacrificar credibilidade.

**Status atual:** **AUTHORIZED FOR LIMITED LEARNING** pela
[Decision 003](decisions/003-phase-4-limited-learning.md). Esta autorização não
é `go`, não valida a tese e não autoriza a Phase 5.

**Slice 4.1 implementada:** `prodmap baseline` oferece somente
uma referência de telemetria anterior exata e consultada sob demanda. Ela não
persiste baseline, não detecta regressão e não produz causalidade, `HIGH` ou
`EXACT`; insuficiência, ambiguidade, contaminação e dados futuros resultam em
`UNKNOWN`.

Entregas:

- baselines com confiança própria;
- comparação anterior e histórica equivalente;
- amostras mínimas, cobertura e tamanho do efeito;
- detecção de eventos concorrentes;
- `regression`, `evidence` e `explain` completos;
- corpus rotulado de falsos positivos e negativos.

**Gate:** metas de precisão definidas no início da fase e atingidas no dataset de validação.

### Phase 5 — Investigation Packages

**Objetivo:** tornar análises portáteis e reproduzíveis.

Entregas:

- exportação sanitizada;
- snapshot de evidências, configuração relevante e versões dos algoritmos;
- schemas estáveis;
- comparação de investigações.

**Gate:** terceiro consegue reproduzir a explicação sem acesso às fontes originais.

### Phase 6 — MCP

**Objetivo:** oferecer contexto operacional seguro a coding agents.

Entregas:

- servidor MCP somente leitura;
- tools de contexto, timeline, regressão e evidência;
- redaction, limites, paginação e auditoria;
- threat model e testes contra dados não confiáveis;
- benchmark agente bruto versus agente + Prodmap repetido.

**Gate:** ganho demonstrado e nenhuma exposição indevida no conjunto de testes.

### Phase 7 — Extensibility

**Objetivo:** provar neutralidade sem virar uma coleção descontrolada de integrações.

Entregas:

- SDK/contratos documentados;
- contract test suite;
- segunda fonte de deployment e segunda fonte de telemetria escolhidas por demanda;
- avaliação de integração MiniPaaS.

**Gate:** nova source implementada sem mudanças indevidas no domínio.

### Phase 8 — Operational Maturity

**Objetivo:** preparar uso contínuo e releases estáveis.

Entregas:

- retenção e compactação;
- backups e recuperação da persistência;
- upgrade/migration testing;
- hardening de performance e segurança;
- documentação operacional e política de compatibilidade;
- release candidate de v1.0.

---

## 19. Métricas de sucesso

### 19.1 Produto

- tempo mediano até diagnóstico correto;
- redução de consultas às fontes;
- precisão/recall de regressões candidatas no corpus rotulado;
- taxa de correlações aceitas, rejeitadas ou corrigidas;
- distribuição e calibração dos níveis de confiança;
- tempo até o primeiro valor com e sem OTel;
- percentual de instalações que concluem `doctor` e primeiro inventário.

### 19.2 Qualidade

- taxa de falso positivo por serviço e perfil de tráfego;
- cobertura de proveniência ponta a ponta;
- percentual de conclusões com evidências reproduzíveis;
- falhas de ingestão e dados descartados;
- cardinalidade e consumo de recursos;
- incidentes de segurança ou redaction.

Métricas de vaidade, como número bruto de integrações ou quantidade de MCP tools, não definem sucesso.

---

## 20. Fora de escopo inicial

- interface web completa;
- armazenamento central SaaS;
- execução automática de rollback;
- alteração automática de código ou infraestrutura;
- root-cause analysis apresentada como certeza;
- ingestão geral de logs sem objetivo correlacional;
- Kubernetes, multi-cloud e grandes vendors no primeiro MVP;
- LLM embutido no núcleo;
- suporte irrestrito a plugins de terceiros antes de contratos estáveis.

---

## 21. Riscos e mitigação

| Risco | Consequência | Mitigação |
|---|---|---|
| Contexto correlacionado não supera acesso bruto | Tese fraca | Phase -1 e experimentos repetidos |
| Onboarding exige stack madura demais | Baixa adoção | Valor com Git + Docker e starter OTel opcional |
| Falsos positivos em regressão | Perda de confiança | Baseline confidence, histórico equivalente e corpus rotulado |
| Correlação temporal tratada como causalidade | Decisões erradas | Linguagem explícita, evidências e contradições |
| Cardinalidade de telemetria | Custo e lentidão | Limites, agregação, retenção e testes adversariais |
| Vazamento de dados para agentes | Incidente de segurança | Local-first, redaction, escopo e auditoria |
| Explosão de integrações | Manutenção insustentável | Escopo inicial rígido e contratos comuns |
| Acoplamento ao GitHub ou MiniPaaS | Perda de neutralidade | Ports orientadas ao domínio e contract tests |
| Grafo desatualizado | Diagnóstico enganoso | validade temporal, freshness e confidence |
| Scores arbitrários | Falsa precisão | especificação versionada, calibração e exibição das evidências |

---

## 22. Critérios para v1.0

Uma v1.0 só deve ser declarada quando:

- a tese tiver evidência experimental documentada;
- Git + Docker funcionarem como caminho mínimo útil;
- ao menos um fluxo completo de deployment e telemetria for suportado;
- correlações exibirem evidências e confiança versionada;
- baseline e correlação tiverem confianças separadas;
- regressões atingirem metas publicadas no corpus de referência;
- MCP passar por threat model, redaction e testes de limites;
- schemas, migrações e política de compatibilidade estiverem documentados;
- instalação, upgrade, backup e recuperação forem testados;
- práticas de qualidade e segurança em Go forem obrigatórias na CI.

---

## 23. Demonstração que define o produto

```bash
$ prodmap regression --deployment production-182

Regression candidate: checkout-api / POST /checkout

Deployment: production-182
Commit: a81f23 — optimize checkout queries
Started: 4m after deployment

p95        182 ms → 941 ms
error rate 0.17%  → 4.72%

Affected dependency: postgres

Baseline confidence: MEDIUM
Correlation confidence: HIGH

Run `prodmap evidence reg_01H...` for details.
```

Então, em um coding agent:

```text
> Investigue a regressão do checkout usando o Prodmap.

The running image maps exactly to commit a81f23. The latency change
started four minutes after its deployment. Database spans account for
most of the increase, but the historical baseline has medium confidence.
I will inspect the query changes in that commit and treat the deployment
link as a strong correlation, not proven causality.
```

Essa demonstração só é válida se o Prodmap puder explicar e reproduzir cada afirmação.

---

## 24. Próximas decisões

Antes de iniciar a Phase 0:

1. escolher o projeto e os incidentes do Experimento 001;
2. definir métricas e limiares de sucesso antes de executar o teste;
3. produzir manualmente o primeiro investigation package;
4. validar se o conteúdo reduz exploração e melhora o diagnóstico;
5. formalizar a primeira versão da Correlation Model Specification;
6. confirmar o caminho mínimo Git + Docker;
7. decidir go, pivot ou stop com base nos resultados.

O primeiro milestone do Prodmap não é um binário. É evidência de que o contexto correlacionado merece virar produto.
