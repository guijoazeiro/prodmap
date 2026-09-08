# ADR-031 — Redaction orientada pelo campo

## Status

Accepted

## Decision

Palavras como `token`, `password`, `authorization`, `bearer`, `secret` e
`cookie` não são, isoladamente, credenciais. A política central
`internal/redaction` separa nome de campo, categoria do valor e estrutura do
valor: campos reconhecidamente sensíveis são recusados por nome; identificadores
legítimos como `token-service` são aceitos; e textos públicos são recusados
somente por padrões de alta confiança, como headers Authorization, atribuições
explícitas, DSNs, URLs com userinfo, chaves privadas, JWTs e paths locais.

As rejeições são sanitizadas e não reproduzem o conteúdo recusado. Não há
redaction parcial silenciosa: material sensível torna o artefato ou ledger
inválido. A política é compartilhada pelo ledger e pelo pacote de investigação,
preservando a proteção em CLI e MCP; validadores específicos de transporte,
OCI, GitHub e ZIP permanecem locais por terem contexto distinto.
