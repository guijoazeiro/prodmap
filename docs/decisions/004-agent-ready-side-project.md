# Decision 004 — Agent-ready side project

## Status

Accepted

## Owner authorization

> Autorizo as Phases 5 e 6 como evolução técnica do side project, limitadas a investigation view, pacote reproduzível e MCP mínimo somente leitura. Isso não constitui validação comercial nem decisão formal go, pivot ou stop.

## Decision

Phases 5 and 6 are authorized as a bounded technical evolution of the side
project: an investigation view, a reproducible package, and a minimal
read-only MCP. This does not authorize a new analytical algorithm, persistence,
migrations, raw-source export, causal claims, commercial validation, or a
formal `go`, `pivot`, or `stop` decision.

## Scope

- `prodmap investigate` accepts a deployment and an existing comparison metric.
- It returns versioned, redacted, deterministic references to existing results.
- It preserves `UNKNOWN`, confidence boundaries, pagination limits, and
  `causality_claimed: false`.

## State

- Product thesis: INCONCLUSIVE.
- Formal `go` / `pivot` / `stop`: NOT MADE.
- Slice 5.1: IMPLEMENTED as a bounded, read-only side project.
- Slice 5.2: IMPLEMENTED as a reproducible, sanitized, offline-verifiable
  package without persistence or raw sources.
- Phase 6: IMPLEMENTED as a minimal local stdio MCP with exactly one read-only
  `investigate_deployment` tool; it has no raw sources, persistence, or
  migration.
