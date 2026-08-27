# ADR-020 — Offline deployment ledger

## Status

Accepted for Phase 3 Slice 3.1.

## Decision

Prodmap accepts the frozen, versioned `deployment-ledger-jsonl/v1` format through
`prodmap deployments ingest --file <path>` and exposes registered deployments
through `prodmap deploys`. Each JSONL line is one deployment record; the loader
reads and validates the complete regular, non-symlink file before SQLite opens.

The loader limits files to 16 MiB, lines to 1 MiB, and records to 10,000. It
rejects ambiguous JSON (including duplicate keys, trailing values, unknown or
missing fields), invalid UTF-8, controls, unsafe values, and schema versions other
than `1.0`. It hashes the content and a normalized absolute path separately; the
path is never persisted or returned.

The SQLite ingestion is all-or-nothing. Replay of the same source key and content
hash returns the original ingestion and statistics. A ledger may append records:
already-known semantic records are counted as existing, while new records are
inserted. Reuse of `(source, deployment_id, service)` with a different fingerprint
is a conflict and rolls back the whole operation. A shared deployment ID is a
group/external ID, not a unique deployment row by itself.

The slice materializes `deployment_ingestions`, `deployments`, and
`deployment_evidence` in migration `000004_deployment_ledger.sql`. Build is not an
entity or table in this slice. The algorithm version is `deployment-ledger/v1`.

Artifact and commit resolution only queries already-persisted artifacts and
commits. A mutable image reference is never identity; a verified commit and an
immutable digest/image ID may be resolved, but missing data remains `UNKNOWN` and
does not create artifacts or commits. There is no runtime correlation in this
slice. Confidence is non-causal and never `EXACT`.

Deployment temporal queries use `[since, until)` and deterministic keyset
pagination. Redaction happens before persistence; raw JSON, source paths,
credentials, environment variables, bodies, tokens, and external error output do
not cross the source boundary.

## Consequences

The ledger is append-only source evidence, not proof that a deployment reached or
is healthy in runtime. `running` is retained as recorded; it is not upgraded from
an application health signal. Phase 4, baseline, regression, timeline, remote
sources, and deployment/runtime correlation remain blocked.
