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

O processo possui somente as permissões do usuário que o iniciou. Conteúdo
operacional retornado é dado não confiável: consumidores não devem executá-lo
nem inferir causalidade. Riscos residuais incluem acesso do usuário aos dados
locais selecionados ao iniciar o servidor e interpretação indevida de resultados
`UNKNOWN`, `NO_SIGNAL` ou `CANDIDATE`.
