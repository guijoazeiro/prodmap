# Phase 4 Gate Review — Reliable Regression Detection

## Status

- **Phase 4 limited technical gate: PASS WITH LIMITATIONS**
- **Product accuracy/calibration gate: NOT ESTABLISHED**
- **Product thesis: INCONCLUSIVE**
- **Formal go/pivot/stop: NOT MADE**
- **Phase 5: BLOCKED**

This review closes the limited-learning authorization in
[Decision 003](../decisions/003-phase-4-limited-learning.md). It is neither a
formal decision nor an authorization for Phase 5.

## Reviewed references

- Regression classification: commit `c4d9dab`.
- Service-level telemetry windows: commit `04becb6`.
- Reference validation: [Phase 4 Slice 4.4](../validation/phase-4-reference-validation.md).
- Evidence package hashes: the seven SHA-256 values recorded in that validation.

## What the technical gate proved

- Algorithms are versioned, with distinct baseline and regression confidence.
- Replay is deterministic and preserves analytical keys.
- `UNKNOWN` represents insufficiency, future evidence, contamination, and
  concurrency rather than a negative conclusion.
- Temporal boundaries are half-open; JSON is versioned and redacted.
- The healthy scenario did not produce a false `CANDIDATE`.
- `payment-latency-v1`, using contract-valid `--min-samples 4`, produced
  `AVAILABLE` / `CANDIDATE` / `LOW` / `INCREASE`.
- No result asserted causality or emitted `HIGH`, `EXACT`, or `CONFIRMED`.

The same package returned `UNKNOWN` at `--min-samples 8` because its exact
post-deployment payment service window contains four SERVER spans. That is a
successful insufficiency safeguard, not a healthy or negative conclusion.

## What the gate did not prove

- Precision or recall, threshold calibration, or statistical validity from four
  samples.
- An equivalent historical baseline or a broad representative corpus.
- Complete regression `evidence` / `explain` outputs.
- Superiority over an agent with raw observability access.
- Causality.

Accordingly, the technical flow is demonstrated, but the product accuracy and
calibration gate is not established and the product thesis remains
inconclusive.

## Recommendation

1. End Phase 4 implementation and freeze new features.
2. Run a manual comparison of an agent using raw data with an agent using
   Prodmap outputs.
3. Do not automatically authorize Phase 5; require a new explicit owner
   decision after that comparison.
