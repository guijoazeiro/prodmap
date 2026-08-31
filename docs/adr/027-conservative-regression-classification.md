# ADR-027 — Conservative regression classification

Status: accepted for bounded Phase 4 Slice 4.3 learning

`regression-threshold/v1-experimental` classifies a read-only comparison as
`UNKNOWN`, `NO_SIGNAL`, or `CANDIDATE`. A candidate is not confirmed and no
result claims causality. Latency requires both +50ms and +20%; error rate
requires +0.05. `request_count` is unsupported. Thresholds are experimental,
there are no scores or weights, Slice 4.4 remains pending, and Phase 5 stays
blocked.
