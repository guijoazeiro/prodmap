# Phase 3 Technical Gate Review — Deployment Intelligence

## Status

- **Phase 3 technical gate: PASS** — reviewed repository state, offline evidence, and the verified controlled remote smoke.
- **Product thesis: INCONCLUSIVE**
- **Formal Phase -1 decision: NOT MADE**
- **Phase 4 authorization: BLOCKED PENDING OWNER DECISION**

This is a technical review of [Decision 002](../decisions/002-phase-3-limited-learning.md), not a formal `go`, `pivot`, or `stop`; it does not authorize Phase 4. The GitHub connector independently verified the controlled remote smoke described below. No unrecorded intermediate output is inferred from it.

## Immutable references

| Subject | Reference | Status |
| --- | --- | --- |
| Review date | 2026-08-28 | Recorded |
| Prodmap | `dev` / `origin/dev` at `8fb6d2265cf320df99116919019c028606da5e73` | Clean and aligned |
| Reference application | `main` / `origin/main` at `5b297daa09a25b51a2d8af51f45073811ac38326` | Clean and aligned |
| Pinned Prodmap workflow revision | `8fb6d2265cf320df99116919019c028606da5e73` | Declared in workflow |
| GitHub workflow | `ci`, run `33197918230` / number 12, `workflow_dispatch`, completed `success` | [Run URL](https://github.com/guijoazeiro/prodmap-reference-app/actions/runs/33197918230) |
| Workflow time | Started `2026-08-28T18:08:14Z`; completed `2026-08-28T18:13:56Z` | GitHub connector |
| Jobs | `validate` `98939713724`: success; `smoke` `98940068881`: success | GitHub connector |
| Artifact | `prodmap-deployment-ledger`, ID `9696685511`, 3132 bytes | Created `2026-08-28T18:13:49Z` |
| Artifact digest | `sha256:ca1ad121c5fe02eab5599a86525f686f441877b0e51328dd1f9a5956fc0d3550` | GitHub connector/log |
| Artifact expiry | `2026-09-04T18:13:48Z`; non-expired when validated | Expiry does not invalidate recorded metadata |
| Logical artifact and retention | `prodmap-deployment-ledger`; exactly `deployments.jsonl`; 7 days | Workflow/staging contract |
| Migrations | `000001_foundation.sql` through `000005_github_actions_deployment_source.sql` | No unexpected migration |
| Versions | `deployment-ledger-jsonl/v1`, `sha256-v1`, `deployment-ledger/v1`, `deployment-runtime/v1` | Contract/code |

`git merge-base --is-ancestor` confirms that Prodmap HEAD contains `f3e0d4f` (3.1), `cd6e81b` (3.2), `f5d6ab2` (3.3), `54012ce` (3.4A), and `8fb6d22` (3.4B1). Reference HEAD contains `db70f47`, `29dd800`, `4122a67`, `1d45be4`, and `5b297da` for 3.4B2.

## Slice assessment

### 3.1 — Offline deployment ledger — PASS

The [ledger contract](../contracts/deployment-ledger-jsonl-v1.md), ADR-020, and migration `000004_deployment_ledger.sql` provide atomic versioned JSONL ingestion. They separate deployment, artifact, and commit; use semantic `sha256-v1` fingerprints for idempotent replay; redact source input; and bind cursor queries to `[since, until)`. Future provenance remains `UNKNOWN`.

### 3.2 — Deployment/runtime association — PASS

ADR-021 and `deployment-runtime/v1` derive the relation at read time; no derived association is persisted. It requires environment, logical service, immutable identity, and `[valid_from, valid_to)` evidence; a tag is not identity. Missing evidence is `UNKNOWN`, mixed/pre-existing fleets are `PARTIAL/MEDIUM`, `MATCHED/HIGH` requires immutable evidence, and `EXACT` is never emitted. Bounds are 100 candidates per deployment and 10,000 per page. Controlled flows cover direct `MATCHED/HIGH` and `UNKNOWN → MATCHED/HIGH` without ledger reingestion.

### 3.3 — Timeline — PASS

ADR-022 defines a dynamic non-persisted timeline of closed event kinds. It preserves declared rollback and concurrency without asserting runtime effect or causality. It uses `[since, until)`, keyset pagination, and `time DESC`, `priority ASC`, `ID ASC`. One `generated_at` controls envelope/item time; future timestamps degrade confidence to `UNKNOWN`; freshness cannot become negative. Tests cover equal-time priority and ID ordering.

### 3.4A — GitHub Actions source boundary — PASS

`internal/github` implements a consumer-local `DeploymentSource`, not a global ports abstraction. It has deterministic pagination/selection, HTTPS redirects, cross-origin Authorization removal, cancellation, bounded retries, and metadata/ZIP/ledger limits. Offline tests cover unsafe ZIP inputs, exactly one `deployments.jsonl`, ZIP digest, decompressed-ledger `SourceHash`, workflow HEAD, redaction, and deterministic HTTP behavior. A workflow run is not a deployment.

### 3.4B1 — Sync CLI and persistence — PASS

`deployments sync github-actions` accepts its token only from `PRODMAP_GITHUB_TOKEN`; invalid input fails before SQLite opens. Migration 000005 writes source observation and ledger atomically. Tests cover replay, immutable conflicts and rollback, sanitized structured JSON, new/upgrade databases, timeout/cancellation, and continued `deployments ingest --file` support.

### 3.4B2 — Publication and controlled smoke — PASS

The manual workflow pins the SHA above and checks out private Prodmap under ignored `.tmp/prodmap` using a distinct read-only `PRODMAP_REPO_READ_TOKEN` and `persist-credentials: false`. It verifies clean reference provenance, stages exactly one private ledger, publishes the stable artifact for seven days, and gives sync only the ephemeral repository token.

**Direct GitHub evidence:** run `33197918230` and jobs `validate`/`smoke` succeeded. The smoke log records `smoke passed: healthy snapshot, observed graph, and payment latency scenario verified`, the artifact ID/digest above, and `github-actions deployment sync verified`. The artifact was non-expired when inspected.

**Versioned-script guarantee:** at reference SHA `5b297daa09a25b51a2d8af51f45073811ac38326`, `scripts/github-sync-smoke.sh` emits its final success only after checking run/artifact identity, first synchronization, idempotent replay, `deploys`, `timeline`, temporal ordering, ascending ID tie-break, no `EXACT`, no invented causality, redaction, and temporary cleanup.

**Grounded inference:** because the remote job succeeded and emitted that final script success, those versioned checks passed for this controlled artifact/run. This does not invent unlogged intermediate values or generalize beyond the supported scenario. Offline tests also passed: `deployment_artifact_test.sh`, `github_sync_smoke_test.sh`, and `worktree_provenance_test.sh`.

## Migrations and invariants

`000001`–`000003` remain historical Foundation/runtime/OTel migrations. `000004` is the offline ledger and `000005` the GitHub Actions source observation. No sixth migration exists; reviewed tests cover new and upgraded databases.

| Invariant | Assessment |
| --- | --- |
| `UNKNOWN` ≠ `LOW` | Preserved for absent or insufficient evidence. |
| Correlation ≠ causality | Preserved: relations are inferred and non-causal. |
| Absence ≠ negative evidence | Preserved: no usable runtime is `UNKNOWN`. |
| Mutable tag ≠ identity | Preserved: only immutable IDs match. |
| Candidate ≠ confirmed | Preserved: bounded candidates do not assert fact. |
| Source observation ≠ deployment | Preserved by separate records. |
| Remote artifact ≠ ledger | ZIP digest remains separate from `SourceHash`. |
| Workflow run ≠ deployment | Preserved by the staged JSONL contract. |
| Declared ≠ observed | Deployment/rollback and runtime remain distinct. |
| No analytical `EXACT` | Preserved by association/timeline contracts. |

## Technical gate criteria

| Criterion | Evidence | Result | Limitation |
| --- | --- | --- | --- |
| Reproducible source | Versioned ledger, fingerprint/source hash, deterministic selection | PASS | One contract/source. |
| Idempotent replay | Deployment/SQLite and B2 offline tests | PASS | Remote run is external evidence. |
| Entity separation | 000004/000005; ADR-020/023/024 | PASS | Build deliberately absent. |
| Temporal behavior | Half-open intervals, ordering/keyset/boundary tests | PASS | Supported windows only. |
| Declared rollback | Closed timeline kinds | PASS | Not observed causal rollback. |
| Concurrency | Timeline ordering/concurrency tests | PASS | No performance inference. |
| Runtime/deployment correlation | ADR-021 and controlled flows | PASS | Inferred, never `EXACT`. |
| GitHub Actions source | ADR-023/024 and offline HTTP tests | PASS | Remote run is external evidence. |
| Versioned JSON | Ledger v1 and CLI envelope tests | PASS | Future evolution out of scope. |
| Cardinality/size limits | Candidate, metadata, ZIP, ledger, line/page limits | PASS | Not capacity certification. |
| Redaction | Source/CLI/B2 script tests | PASS | Remote logs are external evidence. |
| Local smoke | Reference local smoke and B2 scripts | PASS | Controlled environment. |
| Remote smoke | GitHub run `33197918230`, successful jobs/logs, and versioned B2 script | PASS | One controlled reference scenario. |
| Migrations | 000001–000005; new/upgrade tests | PASS | No general policy review. |
| Offline testability | HTTP fixtures and scripts without GitHub | PASS | Does not replace live checks. |
| No invented causality | ADRs, contracts, tests | PASS | Regression scoring not assessed. |

## What Phase 3 proved

Within its bounded technical scope, Prodmap can ingest declared deployments, preserve immutable provenance, synchronize a validated ledger through a GitHub Actions boundary, represent deployment and runtime events temporally, and correlate them only when bounded immutable evidence exists. Supported replays and queries are deterministic.

## What Phase 3 did not prove

It did not prove that deployments caused performance changes; that correlation improves diagnosis statistically; that baseline or regression scoring is reliable; that GitHub Actions covers every deployment model; that the product/commercial thesis is validated; that the directional pilot produces `go`/`pivot`/`stop`; or that Phase 4 should begin.

## Debt, limits, and decision boundary

### Blocking for Phase 4

- An explicit owner decision is required: the thesis is inconclusive and the Phase -1 decision is not made.
- Baseline, regression, scoring, and their own gates are not implemented or authorized.

### Non-blocking for this gate

- Scope intentionally covers one ledger contract and one remote source boundary.
- The artifact expires on its configured schedule; its recorded run metadata and digest remain historical evidence.

### Experimental risk

- Experiment 001 has no completed blinded, pre-registered measurement result.
- Technical correlation cannot establish causation or generalize from the reference scenario.

### Operation/documentation

- Remote smoke needs a separately maintained least-privilege `PRODMAP_REPO_READ_TOKEN`; its value must never enter files or logs.
- The recorded workflow URL/ID, artifact ID, and digest should accompany any later operational claim that relies on this scenario.

## Owner options

No option is selected by this review.

1. **STOP:** end investment after Phase 3 and preserve its learning.
2. **PIVOT:** narrow the proposition to inventory, topology, and deployment intelligence.
3. **CONTINUE-FOR-LEARNING in Phase 4:** make a new explicit, bounded authorization for baseline/regression slices, budget, stop criteria, and gate; it would still not be a formal `go`.

Technical recommendation: continue only through option 3 and a new owner authorization after recording remote evidence and Phase-4-specific interruption criteria. This is not authorization.

## Final state

**Phase 3 technical gate: PASS**

**Product thesis: INCONCLUSIVE**

**Formal Phase -1 decision: NOT MADE**

**Phase 4 authorization: BLOCKED PENDING OWNER DECISION**
