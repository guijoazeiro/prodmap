# ADR-027 — Conservative regression classification

Status: accepted for bounded Phase 4 Slice 4.3 learning

## Context

The deployment-centered comparison remains a mechanical, read-only comparison
of exact telemetry windows. Classification is a separate conservative
interpretation of that comparison: comparison is not classification, and a
classification is not confirmation. Comparison confidence and classification
confidence are independent fields with their own algorithm versions.

Temporal proximity does not establish causality. `NO_SIGNAL` does not prove
healthy behavior, and absence of eligible evidence produces `UNKNOWN`; it is
not negative evidence. This bounded learning decision is not a formal `go`,
`pivot`, or `stop` decision. Phase 5 remains blocked.

## Decision

`regression-threshold/v1-experimental` classifies an immutable, query-time
comparison as exactly one of:

- `UNKNOWN`;
- `NO_SIGNAL`;
- `CANDIDATE`.

Its direction is exactly one of `INCREASE`, `DECREASE`, `UNCHANGED`, or
`UNKNOWN`. Direction describes the observed absolute delta; it is not a
classification.

Latency `p50`, `p95`, and `p99` produce `CANDIDATE` only when both inclusive
thresholds hold: `absolute_delta >= 50,000,000` nanoseconds and
`relative_delta >= 0.20`. Error rate produces `CANDIDATE` when the inclusive
`absolute_delta >= 0.05` threshold holds; relative delta is not required and a
zero baseline is valid. The implementation permits at most two ULPs below
`0.05` only to preserve the decimal threshold after binary floating-point
subtraction; it does not change the contractual threshold.

`request_count` remains `UNKNOWN`: this slice has no reliable threshold for it
and does not invent a request-rate metric.

`UNKNOWN` is returned for valid but analytically insufficient input, including
an `UNKNOWN` comparison, insufficient confidence, unavailable before or after
side, missing required delta, contamination, concurrency, truncation, future
evidence, and a metric unsupported by the classifier. A required delta that is
absent in an analytically insufficient comparison can therefore result in
`UNKNOWN`. A `NaN` or Infinity delta is structurally incompatible input and is
rejected before classification; `NaN` and Infinity are never emitted in JSON.

`comparison_key` identifies the mechanical comparison. `classification_key`
identifies the comparison key together with classifier algorithm and version,
thresholds, observed effect, and classification confidence. `generated_at`
does not participate in either key; classification never modifies
`comparison_key`, and a relevant classification change changes
`classification_key`.

## Guardrails

The classifier must not claim causality or emit `HIGH`, `EXACT`, or
`CONFIRMED`. It does not use a score, weights, a regression probability, or
silent candidate selection. `CANDIDATE` is not a confirmed regression.

Classification is calculated on demand. There is no migration `000006`, no
baseline, comparison, or classification table, and no persisted classification
result. Replay is deterministic for the same persisted inputs and command
parameters.

## Consequences

The CLI exposes a structured classification object and separate comparison and
classification confidence. This enables bounded product learning without
authorizing a broader reliability phase, causal inference, or Phase 5.
