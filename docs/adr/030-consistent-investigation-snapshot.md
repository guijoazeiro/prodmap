# ADR-030 — Snapshot consistente para investigações

## Status

Accepted

## Decision

Uma investigação reúne comparação/regressão, topologia e timeline por meio de
um único snapshot SQLite deferred. Leituras autocommit independentes poderiam
combinar partes anteriores e posteriores a uma escrita concorrente; o snapshot
faz cada investigação representar somente um estado lógico do inventário.

O snapshot só nasce de `OpenReadOnly`, que mantém `mode=ro` e
`query_only=1`. Ele não usa `BEGIN IMMEDIATE` nem `BEGIN EXCLUSIVE`, não tenta
adquirir lock de escrita e é encerrado por rollback idempotente após as
consultas. Em WAL, writers concorrentes podem confirmar alterações enquanto o
reader conserva sua visão anterior; uma nova investigação abre outro snapshot
e pode observar o estado posterior.

`investigate` e `package create` abrem um snapshot por composição e o fecham
antes de renderizar ou publicar arquivos. O MCP abre um novo snapshot em cada
`tools/call`, nunca no startup, handshake ou `tools/list`; snapshots não são
compartilhados entre chamadas. `generated_at` é metadado de saída e não define
o snapshot SQLite. Nenhuma persistência analítica foi adicionada.
