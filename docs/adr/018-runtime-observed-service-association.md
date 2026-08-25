# ADR-018 — Runtime and observed service association

Status: accepted for Phase 2 continue-for-learning

## Context

Phase 1 derives runtime inventory from Docker, while Phase 2 derives logical services from OpenTelemetry `service.name`. Accidental equality between an image reference and `service.name` is not evidence of an association. The reference application publishes `org.opencontainers.image.title`, and environment is part of a service identity.

## Decision

Runtime-to-observed-service association requires the same normalized environment, a runtime from an inspected image, the allowlisted OCI label `org.opencontainers.image.title`, a sanitized and normalized title, and exact equality between that title and the OTel logical service key. Deterministic normalization is not fuzzy matching.

`MATCHED` has `HIGH` confidence with basis `allowlisted OCI image title matches OTel service identity in the same environment`. `PARTIAL` means at least one current runtime has that evidence and at least one does not; `HIGH` applies only to the proven subset and limitations identify unverified runtimes. `UNKNOWN` means evidence is insufficient and remains `UNKNOWN`, never `LOW`. This slice never produces `EXACT`.

## Fallback and environment

An image reference remains an operational runtime-inventory identity, but cannot raise `runtime_association` above `UNKNOWN`, even if it happens to equal `service.name`. Container names, fuzzy matching, source URLs, commits, and timing are not association evidence.

`runtime --refresh --environment <value>` assigns that normalized environment to every accepted observation in the refresh; without the flag it uses `default`. The flag remains a query filter. Explicitly empty, oversized, or control-character environments are rejected.

## Scope and persistence

PostgreSQL dependencies do not become OTel services, and Collector or Prometheus runtimes do not become associated without an observed OTel service. No receiver, deployment, causality claim, or fuzzy matching is introduced.

No migration is created. The sanitized title is stored in the existing allowlisted `oci_labels_json`; the association is derived deterministically from persisted services, current runtime instances, artifacts, and OTel records. Domain, persistence, and JSON models remain distinct.

The result is equivalent whether runtime refresh precedes telemetry ingestion or follows it.
