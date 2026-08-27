# ADR-022 — Temporal deployment timeline

## Status

Accepted for Phase 3 Slice 3.3.

## Decision

`prodmap timeline` is a dynamic read model over deployment ledger records and
runtime observations. It has no migration and persists no event projection.
Kinds are closed to deployment status events, `rollback_declared`, and
`runtime_observed`. A declared rollback is not evidence of an effective runtime
rollback.

The query uses `[since, until)`, deterministic `time DESC, priority ASC, id
ASC` ordering, and a signed keyset cursor. Concurrent deployment records remain
individual events. Record confidence describes normalized source reliability,
not causal confidence; every event is non-causal and never `EXACT`. The model
uses allowlisted fields, bounded pages, and redacted output. Phase 4, baseline,
regression, timeline-derived causal claims, and remote/live sources remain
blocked.
