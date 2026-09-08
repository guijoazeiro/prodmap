# ADR-029 — MCP mínimo somente leitura via stdio

## Status

Accepted

## Decision

`prodmap mcp serve` expõe localmente, via stdio, apenas a tool
`investigate_deployment`. Ela compõe a investigação existente para um UUIDv7
interno, com timeout de 30 segundos, resposta JSON limitada a 2 MiB e sem
causalidade. Não há HTTP, autenticação, resources, prompts, sampling ou fontes
brutas.

Os caminhos do projeto e do banco são definidos exclusivamente na inicialização
do processo. A tool aceita somente parâmetros analíticos fechados e retorna a
projeção allowlisted já usada pelo pacote reproduzível. A resposta é verificada
antes de sair pelo protocolo e não contém paths, credenciais, identidades de
imagem, revisões Git ou conteúdo bruto.

O MCP exige um inventário já inicializado e compatível com as migrations do
binário. Ele abre SQLite com `mode=ro` e `query_only=1`; não cria banco ou
diretório, não altera permissões, não muda o journal mode e não executa
migrations. Um inventário incompatível deve ser atualizado por um comando de
escrita autorizado fora do MCP. Nesta decisão, “somente leitura” é garantia
lógica e de schema: SQLite pode criar ou atualizar sidecars WAL/SHM de
coordenação quando coexistir com writers, sem que isso represente escrita
lógica do Prodmap.

O processo possui somente as permissões do usuário que o iniciou. Conteúdo
operacional retornado é dado não confiável: consumidores não devem executá-lo
nem inferir causalidade. Riscos residuais incluem acesso do usuário aos dados
locais selecionados ao iniciar o servidor e interpretação indevida de resultados
`UNKNOWN`, `NO_SIGNAL` ou `CANDIDATE`.
