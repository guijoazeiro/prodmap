# ADR-026 — Deployment-centered exact-window comparison

Status: accepted for bounded Phase 4 Slice 4.2 learning

## Decision

`prodmap regression --deployment <UUID>` reads one registered deployment and
compares exactly two service-level telemetry windows: baseline
`[D-before,D)` and observation `[D,D+after)`. Both intervals are half-open;
nearby, overlapping, partial, endpoint-aggregated, or multiple windows are not
substituted.

Baseline, observation, and comparison confidence remain separate. Their
algorithms are respectively `baseline-previous-window/v1-experimental`,
`observation-exact-window/v1-experimental`, and
`deployment-comparison/v1-experimental`. Each is only `LOW` or `UNKNOWN`; no
confidence score, `HIGH`, or `EXACT` is produced.

For eligible sides, `absolute_delta = observed_value - baseline_value` and
`relative_delta = absolute_delta / baseline_value`. A zero baseline returns
`relative_delta: null`. The slice does not compute significance, effect size,
thresholds, or a regression classification.

`request_count` has unit `requests`, not `requests/second`; therefore its
before and after durations must be equal. The command rejects unequal durations
instead of silently normalizing or introducing a request-rate metric.

Other registered deployments for the same environment and service contaminate
the corresponding half-open interval; another deployment at `D` is recorded as
concurrent. Contamination and concurrency make comparison confidence `UNKNOWN`.
The selected deployment never contaminates its own observation interval.
Absence of a registered deployment does not establish absence of a rollout,
configuration change, incident, or residual effect. Temporal proximity does not
establish causality.

The read model is query-time only. It uses bounded, deterministic SQLite reads,
persists no comparison or regression result, and creates no migration.

## Consequences

The command supplies values and auditable insufficiency for a later slice, but
does not create `CANDIDATE`, `CONFIRMED`, `NO_SIGNAL`, or any causal conclusion.
Phase 4 Slice 4.3 remains unimplemented and Phase 5 remains blocked.
