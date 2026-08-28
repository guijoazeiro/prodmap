# ADR-024 — GitHub Actions deployment sync

Status: accepted for bounded Phase 3 learning

## Context

ADR-023 defines a bounded, authenticated GitHub Actions artifact boundary. The
deployment ledger must now be persisted without making remote transport data,
credentials, URLs, or ZIP contents part of the local product model.

## Decision

`prodmap deployments sync github-actions` accepts only an owner, repository,
and artifact name. It obtains its token only from `PRODMAP_GITHUB_TOKEN`, uses
the consumer-owned `deployment.DeploymentSource` interface, and applies one
bounded operation deadline.

The source result is validated before SQLite is opened. A successful result is
saved atomically: the existing immutable deployment ledger ingestion and a
sanitized GitHub Actions artifact observation commit in the same transaction.
The observation retains logical repository/artifact identity, immutable digest,
workflow run metadata, artifact lifecycle timestamps, and the local observed
and fetched timestamps. It never stores a token, URL, HTTP response, ZIP
contents, or arbitrary workflow output.

Replay of the same source hash is idempotent. Reuse of an artifact ID with a
different repository, name, digest, workflow run, or workflow head SHA is a
conflict and rolls back the complete transaction.

## Consequences

The local `deployments ingest --file` workflow is unchanged. Network failure,
invalid remote data, and missing credentials create neither a database nor a
deployment ingestion. This remains an offline, explicitly-invoked source; it
does not add a receiver, scheduler, deployment conclusion, or causal claim.
