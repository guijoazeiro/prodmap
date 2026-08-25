# Decision 001 — Directional pilot continuation

## Status

Accepted — bounded continuation, not a Phase -1 go decision

Decision date: 2026-08-25.

## Context

Classification: `pilot directional — excluded from formal study — cannot produce go/pivot/stop`.

The pilot explored whether correlated Prodmap context improves diagnosis over raw operational context. Condition A gave an agent raw context; Condition B gave the same raw context plus correlated Prodmap outputs. It was a directional pilot, excluded from the formal study, and did not have the methodological power to produce `go`, `pivot`, or `stop`.

## Observations

Facts:

- Conditions A and B produced essentially equivalent diagnoses.
- No measurable diagnostic gain was observed.
- Condition B made the observed relationship between services explicit.
- Condition B made citation of correlated evidence more direct.
- Condition B exposed the inconsistency between the SERVER span's HTTP 500 and the graph edge's `error_count=0`.
- That inconsistency motivated a separate correction in Prodmap.

Interpretation:

- The pilot demonstrates utility for traceability and correlation auditability.
- It does not demonstrate diagnostic superiority.
- The scenario was too small and simple to test the central thesis adequately.

Finding the inconsistency does not validate the product value proposition.

## Decision

There is no formal Phase -1 decision. The owner authorizes `continue-for-learning`: a bounded internal investment decision to complete Phase 2 for product learning. It is not part of the formal `go`/`pivot`/`stop` set and is not a `go` decision.

Phase 2 must be validated with the real reference application and more representative scenarios. The thesis must be reviewed again at the end of Phase 2. Phase 3 remains blocked until a new explicit decision.

## Authorized scope

- Consolidation of Phase 2A.
- Completion of Phase 2 — Production Graph.
- Runtime-inventory and observed-service integration only where evidence exists.
- Temporal queries for services, endpoints, and dependencies.
- Cardinality controls.
- Validation with the reference application.
- Corrections needed to satisfy the Phase 2 gate.

## Explicitly unauthorized

- Phase 3 — Deployment Intelligence.
- GitHub Actions as a deployment source.
- New deployment sources.
- Baselines.
- Regression detection.
- Definitive scoring.
- MCP.
- An embedded OTLP receiver without a new decision.
- OTLP log ingestion.
- OTLP metrics ingestion.
- Schema expansion without demonstrated need.
- Automation or expansion of the formal study.

## Exit condition

This authorization ends when the Phase 2 gate is reached, evidence shows the work does not produce useful context, or the scope requires entering Phase 3. Reaching the Phase 2 gate requires an explicit review before any further advance.

## Consequences

- The thesis remains unvalidated and product risk remains open.
- Phase 2 is also an instrument for product learning.
- Future conclusions must distinguish diagnostic quality from traceability.
- This record cannot be used to claim scientific or commercial validation.
