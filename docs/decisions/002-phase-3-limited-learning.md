# Decision 002 — Phase 3 Limited Learning Authorization

## Status

Accepted

## Date

2026-08-26

## Context

Phase 2 reached its technical gate, and the Production Graph was validated with
the reference application. The product thesis remains inconclusive: no formal
`go`, `pivot`, or `stop` decision has been made.

[Decision 001](001-directional-pilot-continuation.md) authorized only the bounded
continuation through the Phase 2 gate. Its authorization ended at that gate, so
any work beyond it requires a new explicit owner decision.

The owner has now authorized Phase 3 as a limited learning investment. This is an
implementation authorization with a bounded technical purpose; it neither
validates the central thesis nor changes the absence of a formal Phase -1
decision.

## Owner authorization

> Autorizo a Phase 3 — Deployment Intelligence como investimento limitado de aprendizado.

## Decision

Only the implementation of **Phase 3 — Deployment Intelligence** is authorized.
The work may proceed in small, reviewable vertical slices. This authorization does
not require all Phase 3 capabilities to be delivered in one change.

The authorized work may include:

1. a minimum deployment model;
2. schema and migrations strictly required by a demonstrated slice;
3. a small `DeploymentSource` contract;
4. a first deployment source;
5. the GitHub/GitHub Actions integration anticipated by the Product Spec;
6. an offline, deterministic source for fixtures and the reference application
   when needed for tests;
7. commit → build/artifact → deployment → runtime relations;
8. rollback representation;
9. representation of concurrent deployments;
10. temporal deployment queries;
11. the `deploys` command;
12. the `timeline` command;
13. evidence and confidence for deployment relations;
14. integration with the reference application; and
15. documentation, fixtures, tests, CI, and a real smoke test needed for the
    Phase 3 gate.

## Explicitly unauthorized

The following remain outside this authorization:

- a formal `go` decision;
- any claim that the product thesis has been validated;
- Phase 4 — Reliable Regression Detection;
- baseline;
- regression;
- `BehaviorChange`;
- `RegressionCandidate`;
- definitive statistical scoring;
- causal scoring;
- OTLP metrics;
- OTLP logs;
- a live OTLP receiver;
- generic export;
- complete investigation packages;
- MCP;
- a web interface;
- Phase 5 or later phases;
- new integrations without demonstrated need;
- anticipatory schema for future phases;
- changes to the reference application beyond what Phase 3 validation requires;
- automation or expansion of the formal study; and
- automatic commits by agents.

## Invariants

The Phase 3 implementation must preserve these invariants:

- `UNKNOWN` ≠ `LOW`;
- correlation ≠ causality;
- absence of evidence ≠ negative evidence;
- a mutable tag ≠ identity;
- candidate ≠ confirmed;
- a recorded deployment ≠ a deployment confirmed in the runtime;
- a completed workflow ≠ a healthy application;
- temporal proximity ≠ causality;
- a declared rollback ≠ an effectively observed rollback;
- one workflow run ≠ an artifact identity;
- a commit SHA without an artifact digest does not prove the runtime;
- different environments cannot be correlated;
- future or contradictory timestamps reduce or block confidence; and
- no secret or raw payload may cross a source boundary.

## Architectural constraints

Phase 3 must follow these constraints:

- organize by capability;
- keep interfaces close to their consumer;
- do not introduce a global `ports/` directory;
- do not introduce a generic CRUD Repository;
- do not create an interface solely to wrap a concrete implementation;
- keep domain, persistence, and JSON models independently shaped when needed;
- keep external SDKs out of the domain;
- treat GitHub Actions as a `DeploymentSource`, not as the domain;
- begin with the smallest vertical slice;
- keep published migrations immutable and add new migrations incrementally;
- document temporal query intervals clearly;
- make replay idempotent;
- keep sources read-only;
- require pagination and limits for remote sources;
- redact before persistence; and
- sanitize external errors.

## Proposed vertical slices

This is an indicative plan, not a final implementation contract. Evidence may
adjust ordering, but may not expand work beyond the authorized Phase 3 scope.

### Slice 3.1 — Deployment Ledger Vertical Slice

**Objective:** prove, offline and deterministically:

```text
commit → artifact → deployment → environment → runtime evidence
```

Candidate deliverables:

- a minimum deployment domain;
- a minimum migration;
- a versioned deployment-ledger JSONL format;
- offline ingestion;
- idempotent replay;
- `prodmap deploys`;
- temporal querying;
- evidence and confidence; and
- reference-application validation without mandatory remote access.

### Slice 3.2 — Deployment/Runtime Correlation

**Objective:** associate a recorded deployment with runtime only when compatible
evidence exists.

The slice may consider environment, artifact digest, immutable image ID, commit
revision, time, and contradictions. Missing evidence must remain `UNKNOWN`.

### Slice 3.3 — Timeline and Deployment Events

**Objective:** add `prodmap timeline` for deployments, runtime observations,
rollbacks, and concurrent deployments with deterministic ordering and pagination.
The timeline must not make causal claims.

### Slice 3.4 — GitHub Actions DeploymentSource

**Objective:** add GitHub Actions as the first remote source through the offline
contract already proven by earlier slices.

Required properties include pagination, rate-limit handling, timeout,
cancellation, limited retry, redaction, no persisted token, deterministic HTTP
fixtures, and controlled smoke validation.

## Phase 3 gate

Phase 3 may be considered technically complete only when:

1. at least one supported source produces reproducible deployments;
2. replay does not duplicate deployments or relations;
3. commit, artifact, deployment, and runtime remain distinct entities;
4. the complete chain is explainable when evidence exists;
5. insufficient data results in `UNKNOWN`;
6. rollback is represented without rewriting history;
7. concurrent deployments remain visible;
8. temporal queries respect documented boundaries;
9. `deploys` and `timeline` provide versioned JSON;
10. pagination, limits, timeout, and cancellation are tested;
11. redaction and threat boundaries are validated;
12. the reference application passes a real smoke test;
13. GitHub Actions, if included as a remote source, passes deterministic HTTP
    tests and controlled validation;
14. no baseline or regression functionality was introduced; and
15. an explicit gate review is recorded.

## Exit condition

This authorization ends at the first of:

- the Phase 3 technical gate being reached;
- evidence that the work does not produce useful context;
- a need to enter Phase 4;
- a material scope expansion; or
- owner revocation.

Reaching the Phase 3 gate does not automatically authorize Phase 4.

## Consequences

- Phase 3 is **AUTHORIZED FOR LIMITED LEARNING**.
- The product thesis remains **INCONCLUSIVE**.
- Formal Phase -1 decision remains **NOT MADE**.
- Phase 4 remains **BLOCKED**.
- This result cannot be presented as scientific or commercial validation.
- Each slice requires manual validation before a commit.
- Agents may not commit without manual authorization.
