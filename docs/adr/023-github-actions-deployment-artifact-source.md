# ADR-023 — GitHub Actions deployment artifact source

**Status:** accepted for Slice 3.4A

## Context

A workflow run is not a deployment, and its head SHA is not artifact or runtime
identity. The first remote boundary must therefore transport the existing,
versioned `deployment-ledger-jsonl/v1` rather than derive deployments from run
metadata.

## Decision

Slice 3.4A reads one GitHub Actions artifact named by a logical source
(`owner`, `repository`, and artifact name). The ZIP must contain exactly one
regular `deployments.jsonl` entry. The existing deployment loader validates the
uncompressed ledger; no second GitHub deployment format exists.

The source key is a versioned SHA-256 of the logical source, not artifact ID,
token, ZIP, or signed URL. GitHub's artifact digest verifies ZIP transport;
the deployment `SourceHash` continues to hash the validated ledger content.
Verified ledger revisions must equal `workflow_run.head_sha`; unverified
records remain unverified.

The source uses bounded sequential pagination, deterministic newest-created
selection (then highest artifact ID), bounded GET retries/rate-limit waiting,
and a bounded in-memory ZIP reader. Redirects are limited, require safe HTTPS
destinations in production, and remove Authorization across origins. Tokens,
URLs, bodies, and headers are never returned or persisted.

## Consequences

The implementation is offline-testable with HTTP fixtures, but has no CLI,
SQLite persistence, migration, real GitHub smoke, or workflow publication.
Those integrations are reserved for Slice 3.4B. Phase 4, baseline, regression,
MCP, and causal claims remain blocked.

## Rejected alternatives

- Treating workflow runs as deployments.
- Accepting arbitrary download URLs or forwarding the token to signed hosts.
- Introducing a GitHub-specific deployment ledger or persisting a remote cache
  before the ingestion boundary is integrated.
