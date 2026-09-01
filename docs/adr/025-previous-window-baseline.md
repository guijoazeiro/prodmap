# ADR-025 — Previous-window baseline foundation

Status: accepted for bounded Phase 4 Slice 4.1 learning

## Context

[Decision 003](../decisions/003-phase-4-limited-learning.md) authorizes Phase 4
as limited learning while keeping Phase 5 blocked. Slice 4.1 needs a
conservative baseline that can be inspected without persisting an analytical
conclusion or interpreting a difference as a regression.

## Decision

`prodmap baseline` evaluates exactly one existing telemetry window immediately
before `--at`: `[at-window, at)`. It accepts exactly one service or endpoint
target and one supported metric: `request_count`, `error_rate`,
`latency_p50`, `latency_p95`, or `latency_p99`.

The implementation reads materialized telemetry windows and deployment events
only. It creates no migration, baseline row, comparison, classification, or
regression candidate. A candidate is accepted only when there is exactly one
exact matching window, enough samples, no known insufficient coverage, no
deployment in the same environment and service during `[start,end)`, and no
future timestamp relative to the response generation time.

Service baselines use only service-level windows and never aggregate endpoint
percentiles; averaging percentiles would create an unsupported statistic. The
algorithm likewise never selects a nearby or overlapping window, because a
different interval would silently change the reference population.

An accepted result is `AVAILABLE` with `LOW` confidence. Unknown coverage and
unverified completeness are disclosed as limitations but do not promote or
block that conservative result. Every unavailable or ambiguous condition is
`UNKNOWN`; the algorithm never emits `HIGH` or `EXACT`, a score, or a causal
claim.

The returned `baseline_key` is a SHA-256 fingerprint of the versioned algorithm
and effective query parameters, including the accepted telemetry window ID when
available. This makes replay deterministic without treating the key as stored
state.

`accepted_windows` contains only the single accepted window. Every `UNKNOWN`
result keeps `accepted_windows` empty. Available candidates that are rejected
are included in `rejected_windows`, ordered deterministically by window ID; the
count is the number of entries in that array. Each rejected entry contains its
ID, start and end timestamps, `observed_at`, sample count, coverage ratio when
safe to render, capture-completeness flag, contamination flag, and one closed,
sanitized reason:

- `AMBIGUOUS_EXACT_WINDOW`
- `CONTAMINATED_BY_DEPLOYMENT`
- `INSUFFICIENT_SAMPLES`
- `INSUFFICIENT_COVERAGE`
- `FUTURE_EVIDENCE`
- `INVALID_WINDOW`
- `METRIC_UNAVAILABLE`

## Consequences

This slice supplies an inspectable prior-window reference for later, separately
authorized Phase 4 slices. It does not detect a change, classify a regression,
compare before and after behavior, use historical-equivalent windows, or alter
the Phase 5 block. `AVAILABLE/LOW` is not evidence of normal behavior, and
`UNKNOWN` is not evidence of no behavior. Historical and combined methods remain
unimplemented.
