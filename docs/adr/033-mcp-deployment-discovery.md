# ADR-033 — MCP deployment discovery

## Status

Accepted

## Decision

The local stdio MCP server is `prodmap-mcp/v2` and exposes exactly two tools:
`list_deployments` and the compatible `investigate_deployment`.
`list_deployments` uses the versioned `deployment-discovery/v1` contract to
return an allowlisted local SQLite projection for the flow
`list_deployments → internal UUIDv7 → investigate_deployment`.

Its optional filters are environment, service, status, `since`, `until`, limit,
and opaque cursor. The first page defaults to the half-open interval
`[generated_at - 24h, generated_at)`, a limit of 20, and a maximum limit of
100; intervals cannot exceed 90 days. Results sort by `started_at DESC` then
deployment ID descending. The versioned cursor restores the original effective
filters and keyset position, so later pages do not recalculate their window.

Each item contains only the internal deployment ID, environment, service,
started time, status, strategy, allowlisted provenance confidence and
limitations, and `causality_claimed: false`. No ledger source, external ID,
Git/image identity, raw telemetry, credentials, path, URL, or SQL is exposed.

Every tool call validates its closed input before opening exactly one consistent
`OpenReadOnly` SQLite snapshot. SQLite remains `mode=ro` and `query_only=1`;
there are no migrations, writes, persistence, network access, or causal claims.
Each call has a 30-second timeout and a 2 MiB response limit. Redaction is
applied to the public projection before protocol output.

## Consequences

`investigate_deployment` retains its v0.2 contract. The server has no resources
or prompts, and this decision neither adds remote sources nor authorizes any
third tool, write, remediation, or deployment action.
