# ADR-021 — Deployment/runtime correlation

## Status

Accepted for Phase 3 Slice 3.2.

## Decision

`prodmap deploys` derives runtime association at query time with algorithm
`deployment-runtime/v1`; no conclusion or dynamic evidence is persisted and no
migration is needed. Association requires the same environment and exact logical
service, and compares only canonical immutable image identities. Mutable tags,
container names and commit-only matches are not runtime proof.

The confirmation window is `[deployment.started_at, end)`, capped at 30 minutes.
The earliest of a later deployment for the same service, query `until`, and the
cap ends the window. Ties use this deterministic precedence: next deployment,
then query `until`, then cap. Concurrent
deployments at the same start are `UNKNOWN`. Query `until` limits visible runtime
evidence.

Compatible replicas are counted, not ambiguous. A mixed fleet is `PARTIAL`; an
immutable mismatch or verified commit contradiction is `CONTRADICTED`; no usable
runtime is `UNKNOWN`. If the matching artifact was already active at deployment
start, confidence is capped at `PARTIAL/MEDIUM`. Health and state are context
only; a stopped runtime may still prove an observation. Every relation is
`INFERRED`, never `EXACT`, never causal, and is bounded to 100 candidates per
deployment and 10,000 per page.

## Consequences

New runtime observations enrich existing deployment queries without ledger
reingestion or deployment updates. Phase 4, baseline, regression, timeline and
causal claims remain blocked.
