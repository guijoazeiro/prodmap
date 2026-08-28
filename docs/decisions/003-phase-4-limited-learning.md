# Decision 003 — Phase 4 Limited Learning Authorization

## Status

Accepted

## Date

2026-08-28

## Context

[Decision 002](002-phase-3-limited-learning.md) authorized only Phase 3 as a bounded learning investment. The [Phase 3 Technical Gate Review](../reviews/phase-3-gate-review.md) records its technical PASS without validating the product thesis or making the formal Phase -1 decision. Work beyond the Phase 3 gate therefore needs a new explicit owner decision.

## Owner authorization

> Autorizo a Phase 4 — Reliable Regression Detection como investimento limitado de aprendizado, sem constituir decisão formal go, pivot ou stop. Baseline e regressão devem ser implementados por slices, com confidence explícito, algoritmos versionados, sem causalidade inventada e com um gate próprio ao final. A Phase 5 permanece bloqueada.

## Decision

Only **Phase 4 — Reliable Regression Detection** is authorized as limited learning. This authorization permits small, independently reviewed slices; it does not authorize implementing all slices at once, validate the central thesis, or imply a formal `go`, `pivot`, or `stop`.

## State

- Phase 3 technical gate: PASS
- Product thesis: INCONCLUSIVE
- Formal Phase -1 decision: NOT MADE
- Phase 4: AUTHORIZED FOR LIMITED LEARNING
- Phase 5: BLOCKED
- This authorization is not `go`, `pivot`, or `stop`.

## Authorized scope

The authorized work is limited to Reliable Regression Detection over approved data and sources:

1. explicit temporal baselines;
2. separate baseline confidence;
3. before/after comparison around deployments;
4. separate regression confidence;
5. conservative candidate classification;
6. deterministic, versioned algorithms with explicit experimental parameters;
7. minimum persistence only where a demonstrated slice requires it;
8. idempotent replay;
9. temporal queries with explicit boundaries;
10. baseline/regression CLI and versioned JSON needed by a slice;
11. validation with the reference application;
12. healthy and `payment-latency-v1` scenarios;
13. deterministic real smoke validation; and
14. a formal Phase 4 gate review.

Phase 4 may use the deployment ledger, deployment/runtime association, timeline, supported OTLP/JSONL traces, materialized services/endpoints/windows, the reference application, and the approved GitHub Actions deployment source.

## Explicitly unauthorized

The following remain outside this decision:

- Phase 5 or later phases, MCP, generic export, a live Prodmap receiver, scheduler, or daemon;
- OTLP metrics or logs ingestion, a new observability platform, machine learning, causal inference, or correlation presented as causality;
- weights presented as validated truth or mandatory seasonal baseline without supporting evidence;
- auto-remediation, automatic GitHub checks/statuses, external notifications, or automatic regression confirmation;
- an expanded formal Phase -1 study; and
- an implicit `go`, `pivot`, or `stop` decision.

No scope may expand merely because it would make implementation easier.

## Normative invariants

- `UNKNOWN` ≠ `LOW`; correlation ≠ causality; and absence of evidence ≠ negative evidence.
- candidate ≠ confirmed; baseline confidence ≠ regression confidence; and a deployment near an event ≠ its cause.
- An empty window ≠ healthy behavior; insufficient data ≠ absence of regression; a mutable tag ≠ identity; and replay ≠ a new observation.
- No analytical result may use `EXACT`. Every confidence has level, basis, limitations, and algorithm version.
- Future timestamps degrade results to `UNKNOWN`; temporal contamination reduces confidence or prevents a conclusion.
- Concurrent deployments prevent exclusive attribution. A declared rollback does not prove runtime effect. No detected regression does not prove no real regression exists.
- Results use candidate, association, or evidence language, never proven cause.

## Slice boundaries

### Slice 4.1 — Baseline foundation

Represent temporal baselines, select eligible windows, calculate minimum deterministic statistics, and return explicit baseline confidence or `UNKNOWN` for insufficiency. It includes a versioned algorithm, explicit parameters, sample count, temporal coverage, freshness, limitations, `[start, end)` boundaries, redaction, and deterministic replay. It does not detect regression.

### Slice 4.2 — Deployment-centered comparison

Compare before/after windows around a deployment while keeping baseline and observation separate, calculating deltas, identifying contamination, and producing separate regression confidence. It cannot confirm causality.

### Slice 4.3 — Conservative regression classification

Classify only `UNKNOWN`, `NO_SIGNAL`, or `CANDIDATE`. `CONFIRMED` remains unavailable absent later evidence and a dedicated authorization/contract. Initial weights and thresholds are explicit, versioned, substitutable experimental hypotheses, covered by tests, and never called scientifically validated.

### Slice 4.4 — Reference validation

Validate healthy and `payment-latency-v1` scenarios, replay, boundaries, concurrent deployments, empty windows, low coverage, future timestamps, redaction, and absence of causal/`EXACT` claims.

### Slice 4.5 — Phase 4 gate review

Separate the technical gate from the product thesis, document what was and was not proved, keep Phase 5 blocked, and require another explicit owner decision. Each slice needs separate implementation and validation before a commit.

## Phase 4 technical gate

The gate can pass only with evidence of all of the following:

1. versioned algorithms;
2. deterministic results;
3. idempotent replay;
4. separate baseline confidence;
5. separate regression confidence;
6. `UNKNOWN` for insufficiency;
7. representation of temporal contamination;
8. representation of concurrent deployments;
9. tested temporal boundaries;
10. degradation for future data;
11. no `EXACT`;
12. no invented causality;
13. versioned JSON;
14. redaction;
15. cardinality and size limits;
16. unit, integration, and race tests;
17. deterministic real reference-application smoke;
18. a healthy scenario without material false candidates;
19. conservative detection of `payment-latency-v1`; and
20. manual review before every commit.

Even a technical PASS leaves the thesis potentially INCONCLUSIVE, creates no automatic `go`, keeps Phase 5 blocked, and requires an explicit owner decision.

## Interruption criteria

Phase 4 must pause or be reviewed if the baseline cannot distinguish insufficiency from normality; healthy traffic frequently produces false candidates; latency cannot be distinguished from normal variation; results depend on ingestion order; replay changes results without new evidence; concurrent deployments receive exclusive attribution; causal language is needed to produce value; the algorithm needs out-of-scope data; or complexity grows without verifiable investigative gain.

## Consequences

Phase 4 is **AUTHORIZED FOR LIMITED LEARNING** only. The product thesis remains **INCONCLUSIVE**, the formal Phase -1 decision remains **NOT MADE**, and Phase 5 remains **BLOCKED**. This decision supersedes only the previous current-state block on starting Phase 4; it does not rewrite the historical limits of Decision 002 or the Phase 3 review.
