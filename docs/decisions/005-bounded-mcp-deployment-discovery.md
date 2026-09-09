# Decision 005 — Bounded MCP deployment discovery

## Status

Implemented by Slice 6.1.

## Owner authorization

> Autorizo a evolução técnica limitada do MCP do Prodmap para a versão v0.3, começando pela descoberta somente leitura de deployments.

## Context and current state

`v0.2.1-agent-ready` is complete and published. Hardening H1–H5 is complete,
and the unit, race, vulnerability, and real-integration CI gates are approved.
The commercial thesis remains inconclusive. Version v0.3 is a limited technical
evolution of the side project, not commercial validation or a formal `go`,
`pivot`, or `stop` decision.

[ADR-029](../adr/029-stdio-read-only-mcp.md) records the original v0.2 MCP
milestone accurately: the binary exposed exactly one tool,
`investigate_deployment`. This decision authorized a later evolution. Slice 6.1
now adds only `list_deployments`, preserving `investigate_deployment`; the
resulting binary exposes exactly those two tools.

## Implemented Slice 6.1 scope

Slice 6.1 adds exactly one read-only MCP tool: `list_deployments`. It uses only
the local SQLite inventory to return a sanitized projection of recent
deployments, so an agent can follow this bounded flow:

```text
list_deployments → select internal UUIDv7 → investigate_deployment
```

The tool accepts optional `environment`, `service`, `status`,
`since` (RFC3339), `until` (RFC3339), bounded `limit`, and opaque `cursor`
filters. It may use deterministic ordering, keyset pagination, one read-only
SQLite snapshot per call, field-aware redaction, versioned JSON, and applicable
confidence and limitations.

Its indicative response contains `schema_version`, `generated_at`, effective
filters, `items`, `next_cursor`, and `limitations`. Each item is allowlisted to
the internal deployment UUID, environment, service, started time, status,
strategy, provenance status and confidence, and
`causality_claimed: false`. The final contract is to be closed during Slice 6.1;
this decision does not implement these fields.

## Explicitly prohibited

This authorization does not permit `list_services`, `get_evidence`, `package
diff`, automatic window preparation, new statistical analysis, or threshold
changes. It does not permit MCP writes, deployment creation or updates, Docker
commands, GitHub access, network access, a live receiver, automatic deployment
changes, arbitrary file/path/SQL access, or tool arguments for `project_dir`,
`data_dir`, `path`, `url`, `token`, or SQL.

It also prohibits external IDs, Git SHAs, image references, raw telemetry,
bodies, credentials, raw logs, rollback/remediation automation, and any change
to the reference application. No additional MCP tools are authorized ahead of
their own decision.

## Required invariants

- MCP remains stdio-only and stdout remains protocol-only.
- SQLite opens through `OpenReadOnly`, with `mode=ro` and `query_only=1`; MCP
  creates neither databases nor migrations.
- Every tool call uses a distinct, consistent snapshot; analytical results are
  never persisted.
- No causal claim is invented; `UNKNOWN` is not `LOW`, absence of evidence is
  not negative evidence, and `CANDIDATE` is not `CONFIRMED`.
- Central redaction, response limits, per-call timeouts, closed input schemas,
  the pinned MCP SDK, and compatibility of `investigate_deployment` remain in
  force.

## Slice 6.1 completion gates

1. MCP remains effectively read-only.
2. An absent database is not created.
3. An incompatible database is not migrated.
4. Handshake and `tools/list` open no snapshot.
5. Each call opens and closes its own snapshot.
6. Pagination neither duplicates nor omits items.
7. The cursor is bound to the filters.
8. Invalid input fails before SQLite is queried.
9. Ordering is deterministic.
10. Output contains no prohibited data.
11. `investigate_deployment` remains compatible.
12. Concurrent calls share no transaction.
13. Timeout and response limits remain active.
14. No migration is created without demonstrated need.
15. A real stdio test covers both tools.
16. No network access occurs.

## Consequence

Only `list_deployments` was authorized and implemented for Slice 6.1; its final
contract is recorded in [ADR-033](../adr/033-mcp-deployment-discovery.md). The
decision does not expand into writes, external sources, or more tools.
