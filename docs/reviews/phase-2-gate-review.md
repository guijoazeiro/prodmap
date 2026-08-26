# Phase 2 Gate Review — Production Graph

## Status

Technical gate: PASS

Product thesis: INCONCLUSIVE

Formal Phase -1 decision: NOT MADE

Phase 3 authorization: BLOCKED

This review records the result of the bounded Phase 2 continuation authorized by
[Decision 001](../decisions/001-directional-pilot-continuation.md). It separates
technical acceptance of the Production Graph from validation of the Prodmap
product thesis. It is not a `go` decision and does not authorize Phase 3.

## Scope reviewed

The review covers the following completed, bounded work:

- Phase 1 Foundation: local runtime inventory, Git/artifact provenance, evidence,
  uncertainty states, and the original vertical slice.
- Phase 2A: offline OTLP JSONL ingestion, an observed temporal graph, bounded
  cardinality, redaction, and the `graph` query surface.
- Phase 2 Slice 1: validation of the observed graph against the reference
  application.
- Phase 2 Slice 2: evidence-based association between Docker runtime inventory
  and observed services.
- Phase 2 Slice 3: temporal endpoint context.
- The reference application and the directional pilot used for learning.

The review does not assess deployments, baselines, regression detection, metrics
or logs OTLP ingestion, a live receiver, MCP, or any Phase 3 capability.

## Immutable references and provenance

| Subject | Immutable reference | Status |
| --- | --- | --- |
| Prodmap | `dev` / `origin/dev` at `b571c5d47258b11fa935f81bcc55db18939cea86` | Aligned when reviewed |
| Reference application | `main` / `origin/main` at `e3239cdfb9abd44ecc723d7cd510037f5a36225e` | Aligned when reviewed |
| Experiment repository | `dafba35d98b057be3896ec4dd9fd86538df55c9f` | Commit available; pilot worktree artifacts are not treated as immutable Git evidence |

No durable `/tmp` smoke-log or snapshot hash is asserted by this review. The
pilot repository contains a directional-pilot manifest and registration template,
but the formal study results are not recorded there as completed measurements.
This review therefore reports only the qualitative pilot observations already
accepted in Decision 001, not invented measurements or a new experiment result.

## Technical acceptance criteria

### 1. Reproducible observed graph — PASS

Phase 2A accepts frozen OTLP JSONL input and constructs a bounded, deterministic
observed graph. The reference-application validation reproduced an observed
`checkout-api → payment-api` relation at HIGH confidence and an observed
`checkout-api → postgresql:reference` dependency at MEDIUM confidence. The latter
remains a dependency rather than an unsupported exact runtime/service identity.

The graph is evidence-backed. It does not infer `EXACT` merely from topology or
image-reference coincidence.

### 2. Cardinality control — PASS

The reviewed graph path applies explicit bounded limits and emits truncation
information instead of silently presenting an incomplete graph as complete.
Ordering and selection are semantic and deterministic, so repeated ingestion of
the same frozen input does not make the visible subset depend on generated IDs or
arrival order.

### 3. Services, endpoints, and dependencies — PASS

The reviewed model separates observed services, HTTP/RPC endpoints, and external
dependencies. Endpoint context retains HTTP method and route where evidence
permits it. Dependencies such as PostgreSQL are not promoted into services absent
the required service evidence.

Runtime-to-service association is bounded by evidence and distinguishes
`MATCHED`, `PARTIAL`, and `UNKNOWN`; coincidence alone remains `UNKNOWN`.

### 4. Temporal behavior — PASS

Observed service and endpoint context is stored in temporal windows. The
reference-application smoke validation observed the relevant endpoint during the
start and interior of its window and returned `NO_ACTIVE_WINDOW` at the exclusive
end boundary. This is consistent with the documented half-open interval model.

Overlapping windows remain distinct rather than being silently merged into an
invented aggregate interval.

### 5. Reference-application validation — PASS

The reference application completed the bounded validation path using its aligned
commit named above. The recorded endpoint context included `POST /checkout` for
checkout and `POST /payments/authorize` for payment. The validation exercised the
offline evidence path, observed graph, runtime/service association, and temporal
endpoint query without claiming deployment intelligence or regression detection.

## What Phase 2 proved

Phase 2 proved that the current bounded implementation can:

- ingest the supported frozen telemetry input without a live receiver;
- retain evidence and uncertainty rather than manufacturing certainty;
- construct an observed, temporally scoped graph with cardinality controls;
- expose service, endpoint, and dependency context for the reference scenario;
- associate runtime inventory with an observed service only when the designated
  evidence is present; and
- make an operational inconsistency visible enough to drive a separate,
  test-backed correction.

The last point refers to the graph edge `error_count` inconsistency exposed during
the pilot: a successful CLIENT paired with an associated SERVER reporting HTTP
500/status ERROR originally yielded zero edge errors. The later correction made
the distributed operation count once and treat either associated side's error as
an edge error. That correction is a technical integrity result, not evidence of
diagnostic superiority.

## What Phase 2 did not prove

Phase 2 did not prove:

- that correlated context improves diagnostic correctness over raw observability
  context;
- that it reduces time-to-diagnosis, queries, or retrieved data volume;
- that it generalizes beyond the small reference scenario;
- that it detects regressions or establishes a baseline;
- that a relationship is causal merely because it is correlated or temporally
  adjacent; or
- that the product is ready for commercial, scientific, or Phase 3 advancement.

No Phase 2 result changes the pre-registered criteria of Experiment 001.

## Directional pilot review

The pilot classification remains:

`pilot directional — excluded from formal study — cannot produce go/pivot/stop`

Its two conditions were intentionally asymmetric only by the additional context:

| Condition | Context available |
| --- | --- |
| A — raw context | Raw operational context |
| B — correlated context | The same raw operational context plus correlated Prodmap outputs |

The qualitative observations recorded in Decision 001 and carried into this gate
review are:

- both conditions reached cautious, similar diagnoses;
- Condition B supplied better structural traceability and made the service
  relationship and related evidence easier to cite;
- Condition B surfaced the later-corrected `error_count` inconsistency;
- the scenario was small and simple; and
- none of these observations proves superior diagnosis.

The pilot has no completed, blinded, pre-registered measurement record that could
support a formal comparison. It remains excluded from the formal study and cannot
yield `go`, `pivot`, or `stop`.

## Thesis assessment

Product thesis: INCONCLUSIVE

The central thesis is whether correlation beats raw observability context for the
defined diagnostic task. The evidence at this gate establishes traceability and
technical usefulness in a bounded scenario, but it does not establish a
comparative diagnostic gain. Similar cautious diagnoses in the directional pilot
are insufficient evidence either for superiority or for rejection of the thesis.

Accordingly, this review does not reinterpret the pilot as a formal experiment,
does not manufacture a quantitative result, and does not make a Phase -1
`go`/`pivot`/`stop` decision.

## Decision boundary and owner options

The Phase 2 technical exit has been reached. The product-decision exit has not.
An owner must make one explicit next decision; no path below is automatic.

### Option A — validate the thesis further

Authorize the pre-registered Experiment 001 only after its real environment,
application, incidents or reproducible scenarios, ground truth, blinded
evaluation, and required artifacts exist. This is the only option that can test
the original comparative thesis under its defined methodology.

### Option B — authorize a bounded Phase 3 only with an explicit new decision

An owner may separately authorize a specifically bounded Phase 3 hypothesis and
scope. Such a decision must state why deployment intelligence is needed, which
sources and schema changes are permitted, which acceptance criteria apply, and
which Phase 3 exclusions remain. It cannot be inferred from this technical PASS.

### Option C — pivot or stop

An owner may choose to pivot the product thesis toward traceability/auditability
or stop the thesis. Either choice requires an explicit decision record; neither
is implied by a technically successful Phase 2.

## Recommendation

Do not auto-authorize Phase 3. Keep Phase 3 blocked until an owner selects and
records one of the options above. The recommended next action is Option A: prepare
and execute the formal Experiment 001 only when its required environment and
study artifacts are available. If the owner instead needs deployment intelligence
for a distinct learning question, use Option B and create a new bounded decision
before implementing it.

## Final conclusion

The Phase 2 technical gate passes: the Production Graph is reproducible,
evidence-bounded, temporally queryable, cardinality-controlled, and validated
against the reference application within its authorized scope.

The product thesis remains inconclusive. The directional pilot improved
structural traceability but did not demonstrate diagnostic superiority, and it is
not a formal Experiment 001 result.

The Phase 2 authorization has therefore reached its technical exit condition.
Formal Phase -1 decision: NOT MADE. Phase 3 authorization: BLOCKED pending an
explicit owner decision.
