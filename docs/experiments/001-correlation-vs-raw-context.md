# Prodmap Experiment 001 — Does correlation beat raw observability context?

**Status:** protocol draft; not executed

**Phase:** -1 — Thesis Validation

**Protocol version:** 1.0

**Decision status:** no `go`, `pivot`, or `stop` decision has been made

This document is the preregistered, executable protocol for comparing an agent using raw operational sources with an agent using the same sources plus a correlated Prodmap-style context package. It defines the design and decision thresholds before any observation is collected. It does not report experimental results.

## 1. Hypotheses and objective

### 1.1 Main hypothesis

For the same incident, source snapshot, agent capability, instructions, and resource budget, adding a correlated context package reduces investigation effort and improves diagnostic quality and evidentiary traceability, without materially reducing correctness, compared with access to raw Git, deployment, and observability sources alone.

The hypothesis concerns structured correlation, not proof of causality. A successful answer must distinguish facts, correlations, uncertainty, and missing data.

### 1.2 Null hypothesis

Adding the correlated context package produces no practically meaningful improvement in investigation effort or diagnostic quality over raw sources alone, or any apparent benefit is offset by materially worse correctness, unsupported causal claims, or loss of reproducibility.

### 1.3 Objective

Measure whether pre-correlated operational context helps agents reach a correct, well-supported diagnosis faster and with less source exploration. The experiment also tests whether conclusions remain traceable, reproducible, uncertainty-aware, and safe to disclose.

This experiment does not validate the future Prodmap implementation, regression algorithm, production readiness, or MCP interface. It validates or challenges the product thesis using manually prepared, schema-stable context packages.

## 2. Prerequisites and execution gate

The experiment MUST NOT start until every required item below is available and frozen:

- a real project or reproducible application with Git history, deployment history, and OpenTelemetry data;
- at least ten eligible incident scenarios with independently established ground truth;
- immutable source snapshots covering every scenario;
- a sanitized correlated context package for every scenario;
- source adapters or query tools that expose identical raw data to both arms;
- a fixed agent model/version, system prompt, toolset, inference settings, and budget;
- at least two qualified blind evaluators and an adjudicator;
- an approved redaction profile and completed leakage review;
- a run manifest, randomization schedule, scoring forms, and result store;
- a dry run using a non-study scenario that validates instrumentation without revealing study incidents.

Current prerequisites:

| Item | Required value | Current value |
|---|---|---|
| Project/application | Real or reproducible application | **TBD — requires user-provided experimental environment** |
| Repository snapshot(s) | Immutable commit IDs | **TBD — requires user-provided experimental environment** |
| Deployment source and history | Immutable exported snapshot | **TBD — requires user-provided experimental environment** |
| OpenTelemetry source and windows | Immutable sanitized snapshot | **TBD — requires user-provided experimental environment** |
| Incident catalogue and ground truth | At least ten eligible scenarios | **TBD — requires user-provided experimental environment** |
| Agent model and exact version | Same in both arms | **TBD — requires user-provided experimental environment** |
| Agent inference settings | Fixed and recorded | **TBD — requires user-provided experimental environment** |
| Per-run time, query, and byte budgets | Same in both arms | **TBD — requires user-provided experimental environment** |
| Evaluators and adjudicator | Named before execution | **TBD — requires user-provided experimental environment** |
| Redaction profile approval | Approved before snapshots freeze | **TBD — requires user-provided experimental environment** |

If any prerequisite is missing, the protocol remains executable in design but the experiment status is `blocked`, not `stop`.

## 3. Application and telemetry profile

### 3.1 Required application

The selected application MUST:

- have a Git repository whose relevant commits can be resolved from the frozen snapshot;
- have an auditable deployment-to-artifact-to-revision trail for at least some scenarios;
- produce repeatable traffic or provide immutable incident recordings;
- contain enough service or dependency structure to require non-trivial investigation;
- permit incident truth to be established independently of either experimental arm;
- avoid relying on confidential production data unless it has been explicitly approved and sanitized.

Application identity, topology, runtime, traffic generator, environment, and snapshot hashes are **TBD — requires user-provided experimental environment**.

### 3.2 Minimum telemetry profile

Each scenario MUST include, for a fixed time interval `[start, end)`:

- OpenTelemetry traces with service, operation/route, timestamps, status, and dependency spans;
- aggregate request count, error count/rate, and latency p50, p95, and p99 for relevant services or endpoints;
- deployment events with timestamps, environment, target service, artifact identity, and revision metadata when available;
- Git commits and diffs required to inspect plausible causes;
- runtime artifact identity, preferably an immutable digest;
- explicit coverage, freshness, ingestion gaps, clock skew, and missing-data annotations;
- concurrent deployments, restarts, health failures, and material traffic changes when present.

Raw payload bodies, credentials, authorization headers, unrestricted logs, and unnecessary personally identifiable information MUST NOT be included. Logs are optional and may be included only when required by a preregistered scenario and after redaction.

The exact collector configuration, sampling policy, retention window, clock synchronization tolerance, minimum trace count, and acceptable coverage are **TBD — requires user-provided experimental environment**.

## 4. Experimental design

### 4.1 Unit of comparison

The primary unit is a matched pair: two independent agent sessions investigate the same scenario and frozen source snapshot, one in each arm. Sessions MUST NOT share conversation state, cache, scratch files, or prior outputs.

Each scenario is a blocking factor. Arm assignment and execution order are randomized from a seed recorded before execution. Half of each scenario's matched pairs run control first and half run experimental first. Run IDs MUST not reveal arm to evaluators.

### 4.2 Control arm — raw context

The control agent receives:

- the common incident prompt;
- read-only query access to the frozen Git, deployment, runtime, metrics, and trace sources;
- source schemas and neutral query-tool documentation;
- the same time, query, byte, and context-window budgets as the experimental arm.

It does not receive precomputed links, ranked candidates, correlation scores, confidence levels, evidence summaries, investigation packages, or outputs produced by the experimental arm.

### 4.3 Experimental arm — correlated context

The experimental agent receives everything available to the control agent plus one versioned, sanitized context package generated from exactly the same frozen sources. The package MAY contain:

- entities needed for the scenario: commit, artifact, deployment, runtime, service, endpoint, and dependency;
- explicit relations with source references and validity intervals;
- favorable, contradictory, and neutral evidence;
- confidence dimensions and explanations;
- freshness, missing-data, ambiguity, and limitation notices;
- stable identifiers that resolve back to permitted raw records;
- the package schema and correlation algorithm/manual-construction version.

The package MUST say that correlation is not causality. It MUST NOT contain the ground-truth label, evaluator notes, a recommended final diagnosis, hidden incident annotations, or data absent from the control sources.

### 4.4 Symmetric access rules

Both arms may access:

- identical frozen source records and Git content;
- identical query operations and documentation;
- the incident symptom and investigation question;
- identical compute, time, context, query-count, and retrieved-byte ceilings;
- the same neutral definition of a complete answer.

Only the experimental arm may access the correlated package. Any convenience indexes provided only to one arm count as experimental treatment and MUST be documented in the manifest.

### 4.5 Prohibited information and contamination controls

Neither arm may access:

- ground-truth cause labels or evaluator rubric answers;
- post-incident reports that disclose the cause;
- outputs, tool logs, or scratch data from another run;
- live sources whose content can change during the study;
- the randomization schedule beyond the current run;
- study results or aggregate metrics before all runs are locked;
- undocumented operator hints;
- internet search unless it is a necessary, symmetric, frozen source explicitly preregistered.

Agents, operators, and evaluators MUST use separate workspaces. A run exposed to forbidden information is marked contaminated and excluded under the preregistered invalidation rule; it is not silently replaced except by the next run ID already present in the frozen replacement schedule.

## 5. Incident scenarios and questions

### 5.1 Eligibility and composition

The study requires at least ten distinct scenarios, drawn from at least three incident classes. Scenarios must represent distinct failures or causal episodes; variations of the same failure do not count as independent scenarios. At least one scenario MUST have each of the following outcomes:

- a deployment-associated degradation supported by independent ground truth;
- a degradation with a plausible but incorrect nearby deployment or other confounder;
- insufficient evidence, where `unknown` or multiple hypotheses is the correct conclusion.

The remaining scenarios SHOULD cover at least two of: latency regression, error-rate increase, dependency slowdown, runtime/artifact mismatch, restart or health failure, telemetry gap, or material traffic shift.

Eligible scenarios may be known real incidents or reproducible injected failures. The incident definition and truth package MUST be created before the correlated package. Scenario authors MUST not tune inclusion based on which arm appears likely to win.

The selected scenarios, incident timestamps, classes, and provenance are **TBD — requires user-provided experimental environment**.

### 5.2 Ground truth

Ground truth MUST be established independently through at least two compatible sources, for example a controlled fault injection plus code/configuration evidence, or a verified remediation plus reproducible before/after behavior. Temporal proximity alone is insufficient. Ground truth records MUST distinguish:

- confirmed causal factor(s);
- contributing factor(s);
- plausible but disproven alternatives;
- facts observable from the frozen sources;
- facts intentionally withheld from both arms;
- acceptable `unknown` conclusions.

### 5.3 Common question presented to agents

Use the same prompt in both arms, substituting only scenario identifiers and symptom details:

> Investigate incident `{scenario_id}` using only the provided sources. Identify the most likely cause, affected component and relevant change or event. Distinguish verified facts from correlations and hypotheses. Cite the records supporting and contradicting your conclusion, state material missing data, list alternatives considered, and provide the steps another investigator would need to reproduce your diagnosis. If the evidence is insufficient, say so rather than choosing a cause. Finish with a concise primary diagnosis and confidence statement.

No follow-up hints are allowed. Clarification about tool syntax may use a frozen answer sheet shared by both arms and must be logged.

## 6. Execution procedure

### 6.1 Preparation

1. Approve this protocol and record its Git blob hash.
2. Select eligible scenarios without examining arm performance.
3. An incident owner who will not author the correlated package creates and seals a truth package for each scenario.
4. Export immutable raw-source snapshots and record cryptographic hashes.
5. An author without access to the sealed truth labels produces the correlated package only from those snapshots; record its schema, construction procedure, versions, and hash.
6. Run automated leakage and redaction checks, followed by human review.
7. Freeze the agent version, prompts, tool versions, budgets, stopping rules, randomization seed, and replacement schedule.
8. Train evaluators on separate examples and freeze the scoring guide.
9. Perform one instrumentation dry run on a non-study scenario. Dry-run results are never included.

### 6.2 Run

For each scheduled run:

1. Create a clean, isolated session and verify snapshot hashes.
2. Apply the scheduled arm without revealing the arm in the run ID.
3. Start monotonic timing immediately before the incident prompt is delivered.
4. Capture every query, query result byte count, tool error, agent message, and timestamp.
5. Stop at final answer, time budget, query budget, byte budget, unrecoverable tool failure, or safety violation.
6. Record the final answer exactly; do not edit it for evaluation.
7. Run deterministic artifact validation and redaction checks.
8. Seal the run directory and its manifest hash before the next run.

### 6.3 Minimum execution count

The minimum valid study contains 30 matched pairs: at least ten independent scenarios with three matched pairs per scenario. This means at least 60 scored agent sessions, 30 per arm, excluding the dry run. Each pair uses fresh independent sessions. Replicates estimate agent variability, but inference and consistency treat the scenario—not each replicate—as the independent unit.

If fewer than 30 valid pairs remain, fewer than ten independent scenarios remain, or any scenario has fewer than three valid pairs after preregistered replacements are exhausted, the study is invalid and no `go`, `pivot`, or `stop` decision may be made. Increasing the sample after viewing results is prohibited; a larger sample must be fixed before the first scored run. A power analysis may require a larger preregistered sample once pilot variance from a separate, excluded corpus is available.

## 7. Correctness and blind evaluation

### 7.1 Correct-response criteria

Before runs begin, the truth package for each scenario MUST define an answer key containing required, acceptable, and contradicted claims. A response is `correct` only if all conditions hold:

- its primary diagnosis matches an acceptable ground-truth cause, or explicitly concludes insufficient evidence when that is the keyed answer;
- it identifies the affected component at the required granularity;
- it does not assert a disproven alternative as fact;
- it does not claim causality from correlation alone;
- every decisive claim is supported by a permitted source reference;
- its confidence and uncertainty are compatible with the available evidence.

A partially correct answer is scored in the quality rubric but is not counted as correct for binary accuracy. Evaluators may not infer missing reasoning on the agent's behalf.

### 7.2 Blinding

Two evaluators independently score de-identified answers in randomized order. Arm names, context-package references, raw transcripts, timing, query counts, and run metadata are hidden. References are normalized to opaque IDs while preserving resolvability. Evaluators receive only the answer, scenario prompt, truth package, and scoring guide.

Disagreements on binary correctness or any rubric dimension differing by more than one point go to a third adjudicator. Before adjudication, report inter-rater agreement using percent agreement and Cohen's kappa for binary correctness, and weighted kappa or intraclass correlation for rubric dimensions. Evaluator identities and conflicts of interest are recorded before scoring.

The evaluator roster and conflict declarations are **TBD — requires user-provided experimental environment**.

## 8. Metrics and rubric

### 8.1 Quantitative metrics

| Metric | Operational definition | Aggregation |
|---|---|---|
| Time to correct diagnosis | Seconds from prompt delivery to the first timestamped claim that matches the final correct primary diagnosis and is supported by at least one valid reference; determined from transcript after blind correctness scoring. Incorrect/timeout runs are censored at the run budget and separately counted as failures. | Per scenario and overall paired median; success-rate sensitivity analysis |
| Query count | Number of agent-initiated source/tool invocations, including failed or repeated calls; package delivery is one treatment access, not a raw query. | Paired median and total |
| Retrieved volume | Uncompressed UTF-8 bytes returned by tools plus package bytes initially exposed to the model; schemas/tool boilerplate shared by both arms are excluded once and documented. | Paired median, total, and p90 |
| Cause precision | `1` when the primary causal claim is acceptable, `0` when absent/incorrect; additionally `acceptable primary claims / all distinct primary causal claims` for multi-claim answers. An appropriate `unknown` is correct only in keyed insufficient-data scenarios. | Proportion correct and macro-average by scenario |
| Incorrect hypotheses | Count of distinct hypotheses asserted as likely or factual that the answer key marks contradicted; alternatives explicitly rejected by the agent are not counted. | Paired mean/median and zero-error rate |
| Evidence quality | Rubric dimension below. | Blind mean/median and scenario macro-average |
| Traceability | Rubric dimension below plus percentage of decisive claims resolving to immutable permitted records. | Blind score and resolution rate |
| Reproducibility | Rubric dimension below plus independent replay outcome. | Blind score and replay success rate |
| Blind evaluation | Total rubric score assigned without arm metadata. | Scenario macro-average and paired distribution |

Tool wrappers MUST count queries and bytes mechanically. Truncated responses count the bytes actually delivered. Cached results count when exposed to the agent. Operator time and retries are logged but excluded unless the retry delivered data to the agent.

### 8.2 Quality rubric

Each dimension is scored from 0 to 4 using the frozen answer key. The total is 28 points.

| Dimension | 0 | 1 | 2 | 3 | 4 |
|---|---|---|---|---|---|
| Diagnostic correctness | Wrong and misleading | Mostly wrong | Mixed/partial | Correct with minor omission | Correct at required granularity |
| Cause precision | Unsupported or indiscriminate | Several likely claims | Plausible but broad | Focused with small ambiguity | Correctly focused, or justified `unknown` |
| Evidence quality | No valid evidence | Weak/mostly temporal | Some relevant evidence | Multiple relevant sources | Independent support plus contradictions considered |
| Traceability | Claims cannot be resolved | Few vague references | Some resolvable references | Most decisive claims resolve | Every decisive claim resolves immutably |
| Reproducibility | No usable steps | Major missing steps | Partial replay possible | Replay possible with minor gaps | Independent replay reaches the same evidence-bounded conclusion |
| Uncertainty discipline | False certainty/causal overclaim | Major overstatement | Mixed calibration | Appropriate caveats | Explicit facts, inference, missing data, alternatives, and confidence |
| Clarity/actionability | Unusable | Hard to follow | Understandable but incomplete | Clear investigation narrative | Concise, structured, and directly verifiable |

For dimensions 0–4, evaluators select the lowest anchor whose requirements are fully met. Scenario-specific examples may clarify anchors but may not change them after execution starts.

### 8.3 Replay test

A third party, blind to arm and ground truth, follows the submitted reproduction steps against a fresh copy of the frozen snapshot. Replay succeeds only if the cited records resolve and the steps recover the decisive evidence without undocumented access. At least one randomly selected run per arm per scenario is replayed; selection uses the preregistered seed.

## 9. Analysis and result calculation

### 9.1 Analysis set

Use all valid scheduled runs (`intention-to-treat` by assigned arm). Budget exhaustion and incorrect answers remain in the analysis. Exclude only runs meeting a preregistered invalidation condition: corrupted snapshot, instrumentation failure that prevents required measurement, forbidden-information exposure, wrong arm package, or platform outage affecting only that run. Report every exclusion and replacement.

### 9.2 Calculations

For each matched pair and metric, compute `experimental - control`; for time, queries, and bytes also report relative reduction `(control - experimental) / control`, using paired medians for the primary summary. Zero denominators are reported separately and never replaced with an arbitrary epsilon.

Compute scenario-level summaries first, then macro-average scenarios so a high-volume scenario cannot dominate. Report point estimates and 95% paired bootstrap confidence intervals with resampling stratified by scenario. Report binary correctness with paired differences and confidence intervals. No single p-value determines the decision.

The preregistered primary outcomes are:

1. correct-diagnosis rate;
2. time to correct diagnosis;
3. query count;
4. blind total quality score;
5. evidence-quality and traceability scores.

Retrieved volume, incorrect hypotheses, reproducibility, and agreement are mandatory secondary outcomes. All metrics are reported, including unfavorable ones. No post-hoc subgroup can override the overall decision; exploratory analyses must be labeled.

### 9.3 Practical-improvement thresholds

An outcome has a practically meaningful improvement only when its overall point estimate and scenario macro-average meet the threshold, improvement occurs in at least two thirds of scenarios and in every preregistered incident class represented by three or more scenarios, and the 95% confidence interval excludes material harm defined below.

- time to correct diagnosis: at least 20% lower;
- query count: at least 25% lower;
- retrieved volume: at least 20% lower;
- binary correctness: at least 10 percentage points higher;
- blind total quality: at least 10% higher (2.8 of 28 points);
- evidence quality: at least 0.5 of 4 points higher;
- traceability: at least 0.5 of 4 points higher;
- reproducibility: at least 10 percentage points higher replay success;
- incorrect hypotheses: at least 25% fewer, with absolute reduction reported.

Material harm means correctness lower by more than 5 percentage points, any evidence-quality, traceability, or reproducibility score lower by more than 0.25 points, replay success lower by more than 5 percentage points, or an increased rate of unsupported causal claims by more than 5 percentage points.

## 10. Preregistered `go`, `pivot`, and `stop` thresholds

Apply these rules only to a valid completed study. First test the invalidity rules, then `go`, then `stop`; every remaining valid result is `pivot`. The raw results and confidence intervals must be published before the label.

### 10.1 Invalid execution — no decision

Record `no decision — rerun required` if any of these conditions holds:

- a prerequisite, frozen artifact, or required ground-truth element was absent;
- fewer than ten scenarios, 30 matched pairs, or three valid pairs in any scenario remain;
- raw-source access, tools, budgets, time horizon, or redaction differed between arms beyond the declared treatment;
- the correlated package contained ground truth, a recommended diagnosis, or a claim not resolvable from control-accessible raw data;
- blinding was materially broken or ground truth remained disputed after adjudication;
- more than 10% of scheduled sessions were excluded, or the arm exclusion rates differ by more than 5 percentage points;
- a security or redaction failure could have affected agent behavior or publishable results;
- a material protocol deviation makes any primary outcome unavailable or non-comparable.

Invalidity is not evidence for or against the thesis. Correct the cause, assign a new cohort, freeze new hashes, and rerun without pooling invalid primary results.

### 10.2 `go`

Decide `go` only when all conditions hold:

1. no material harm is observed in correctness, evidence quality, traceability, reproducibility, or unsupported causal claims;
2. correct-diagnosis rate is non-inferior: the lower bound of the 95% confidence interval for `experimental - control` is greater than `-5` percentage points;
3. at least one efficiency outcome (time, queries, or bytes) shows a practically meaningful improvement;
4. at least one diagnostic-quality outcome (binary correctness, blind total quality, evidence quality, traceability, reproducibility, or incorrect hypotheses) shows a practically meaningful improvement;
5. at least two important axes therefore improve, as required by the Product Specification, and neither improvement is supported by only one scenario.

`go` authorizes planning the next phase under its own gates; it does not prove causality, validate every Prodmap feature, or authorize Phase 1.

### 10.3 `pivot`

Decide `pivot` when the `go` conditions are not met and at least one of these applies without a `stop` condition:

- a practical improvement is confined to a specific incident class or to one value proposition such as runtime provenance or traceability;
- efficiency improves but diagnostic quality does not, or diagnostic quality improves while efficiency cost materially increases;
- point estimates reach practical thresholds but uncertainty or scenario consistency is insufficient;
- the correlated package helps evidence retrieval but not cause identification;
- correct use depends on a package format, source quality, or workflow that should change before retesting.

The pivot decision MUST name the revised thesis, retained value proposition, rejected claim, and a new preregistered experiment. It may not relabel an unfavorable result as success.

### 10.4 `stop`

Decide `stop` when any of these conditions holds in a valid study:

- material harm in correctness or unsupported causal claims is observed and its 95% confidence interval does not include the no-harm boundary;
- neither an efficiency outcome nor a diagnostic-quality outcome reaches any practical-improvement threshold, and the confidence intervals exclude the minimum improvements required for `go`;
- raw context is practically superior on at least one efficiency and one diagnostic-quality outcome, with no compensating practical improvement in traceability or reproducibility;
- correlated context repeatedly causes agents to anchor on incorrect relations in at least 25% of experimental runs and at least twice the control-arm rate, with the confidence interval excluding equality.

A technically invalid or underpowered execution produces `no decision — rerun required`, not `pivot` or `stop`.

## 11. Variable controls

The following MUST be identical or deliberately counterbalanced:

- model provider, model identifier/version, inference parameters, system prompt, tool definitions, and context limit;
- source snapshots, source schemas, query semantics, network policy, hardware class, and concurrency;
- run budgets, timeout behavior, retry policy, and stopping rules;
- scenario prompt, answer format, and permitted clarification sheet;
- time of source observation; all data are frozen, not queried live;
- operator intervention; no discretionary hints;
- order, balanced and randomized within scenario;
- package generation procedure and amount of raw data represented.

Record model nondeterminism rather than claiming deterministic agent output. Multiple independent sessions estimate that variability. If the model or any source snapshot changes, assign a new experiment cohort and do not pool it with the original primary analysis.

## 12. Biases, risks, and limitations

- **Scenario selection bias:** known easy wins may favor correlation. Mitigation: eligibility rules, multiple classes, confounded and insufficient-data cases, and scenario freeze before package creation.
- **Package-author leakage:** authors may encode the answer. Mitigation: truth package first, separate package author where possible, automated diff to raw sources, and blind leakage review.
- **Unequal information:** the package may contain facts absent from raw sources. Mitigation: every package claim must resolve to a permitted raw record; unresolved claims invalidate the package.
- **Unequal token budget:** the package consumes context but saves queries. Mitigation: count package bytes, keep total budgets equal, and report byte volume.
- **Learning or memory effects:** prior runs may expose answers. Mitigation: isolated sessions, no shared caches, randomized order, and separate operators/workspaces.
- **Evaluator bias:** style may reveal the arm. Mitigation: de-identification, opaque references, randomized ordering, independent scoring, and adjudication.
- **Model drift:** hosted model behavior may change. Mitigation: exact version/snapshot where available, short execution window, timestamps, and separate cohorts after change.
- **Instrumentation effect:** tool latency may dominate. Mitigation: local frozen sources, identical wrappers, record tool and reasoning time separately.
- **Stopping/cherry-picking:** early favorable results may end the study. Mitigation: frozen sample size, replacement schedule, metrics, and decision rules.
- **Ground-truth uncertainty:** real incidents may not have one cause. Mitigation: multi-source truth, acceptable contributor sets, and explicit unknown cases.
- **Construct validity:** a manual package may outperform or underperform a future product. This experiment tests the information treatment, not implementation quality.
- **External validity:** one application, agent family, telemetry stack, or incident mix cannot establish universal superiority. Any `go` decision remains scoped to the tested cohort.
- **Cost exclusion:** engineering effort to create and maintain correlation is not measured here. A later decision must compare observed benefit with implementation and operational cost.

## 13. Security and redaction

All artifacts are local-first and read-only. Before an agent or evaluator receives data:

- remove secrets, tokens, cookies, authorization headers, DSNs, private keys, payload bodies, and unnecessary PII;
- replace sensitive paths, hosts, account IDs, and user identifiers with stable pseudonyms when identity continuity matters;
- allowlist attributes rather than relying only on denylist matching;
- scan raw snapshots, packages, transcripts, tool outputs, and final answers;
- retain a restricted redaction map outside agent and evaluator workspaces;
- enforce least-privilege filesystem and source access;
- record access and artifact hashes without logging sensitive content;
- stop and quarantine a run on suspected disclosure; do not paste the value into issue reports;
- define retention and secure deletion rules before collection.

The data owner, approved redaction profile, retention period, artifact access list, and incident-response contact are **TBD — requires user-provided experimental environment**.

## 14. Artifact format

Store each experiment under an immutable experiment ID:

```text
experiments/001/<cohort-id>/
  protocol.md
  preregistration.json
  randomization.json                 # restricted until runs finish
  scenarios/<scenario-id>/
    manifest.json
    prompt.md
    truth.json                       # evaluator-only
    raw-sources/<source>/...
    correlated-context.json
    schemas/...
  runs/<opaque-run-id>/
    manifest.json
    transcript.jsonl
    queries.jsonl
    final-answer.md
    measurements.json
    redaction-report.json
  evaluations/<opaque-run-id>/
    evaluator-a.json
    evaluator-b.json
    adjudication.json
    replay.json
  analysis/
    exclusions.json
    metrics.csv
    paired-results.csv
    summary.json
    report.md
  decision.md
  SHA256SUMS
```

JSON artifacts MUST contain `schema_version`, `experiment_id`, `protocol_version`, timestamps in UTC, and relevant source/package/agent hashes. JSONL records use one schema-versioned object per line. Manifests list byte counts, tool versions, budgets, exit reason, contamination status, and parent hashes. Raw and evaluator-only data have separate access controls. Generated analysis MUST be reproducible from sealed inputs using a versioned script; the script location is **TBD — requires user-provided experimental environment**.

## 15. Execution checklist

### Before collection

- [ ] Protocol reviewed, approved, and hashed.
- [ ] All required environment fields and TBDs resolved.
- [ ] Ten or more eligible scenarios and truth packages frozen.
- [ ] Raw snapshots and correlated packages hashed.
- [ ] Package claims resolve to raw source records.
- [ ] Redaction and leakage reviews passed.
- [ ] Model, tools, prompts, budgets, seed, and replacement schedule frozen.
- [ ] Evaluators trained and conflicts declared.
- [ ] Non-study dry run passed instrumentation checks.

### During collection

- [ ] Clean session and correct snapshot verified for every run.
- [ ] Timing, queries, bytes, tool failures, transcript, and exit reason captured.
- [ ] No operator hint or cross-run state introduced.
- [ ] Safety events quarantined and documented.
- [ ] Run artifacts sealed before proceeding.

### After collection

- [ ] Scheduled sample completed without data-dependent stopping.
- [ ] Exclusions match preregistered rules and are fully listed.
- [ ] Answers de-identified before blind scoring.
- [ ] Two independent evaluations and required adjudications completed.
- [ ] Replay sample completed.
- [ ] All mandatory metrics and confidence intervals calculated.
- [ ] Analysis reproduced from sealed inputs.
- [ ] Security scan repeated on publishable artifacts.
- [ ] Decision template completed without changing thresholds.

## 16. Result record template

Copy this section into the cohort report; do not fill it before execution.

```markdown
# Experiment 001 results — <cohort-id>

Protocol version/hash:
Execution dates (UTC):
Application/snapshot hashes:
Agent model/version and settings:
Scenarios and classes:
Scheduled pairs / valid pairs:
Excluded or replaced runs and preregistered reasons:
Budget per run:
Evaluators and agreement statistics:

## Primary outcomes

| Outcome | Control | Experimental | Paired effect | 95% CI | Scenario consistency | Practical threshold met? |
|---|---:|---:|---:|---:|---:|---|
| Correct-diagnosis rate | | | | | | |
| Time to correct diagnosis | | | | | | |
| Query count | | | | | | |
| Blind total quality | | | | | | |
| Evidence quality | | | | | | |
| Traceability | | | | | | |

## Secondary outcomes

| Outcome | Control | Experimental | Paired effect | 95% CI | Notes |
|---|---:|---:|---:|---:|---|
| Retrieved bytes | | | | | |
| Incorrect hypotheses | | | | | |
| Replay success | | | | | |
| Unsupported causal claims | | | | | |

## Per-scenario results

<report every scenario and both arms>

## Safety, contamination, and protocol deviations

<none, or complete list>

## Exploratory analyses

<clearly labeled; may not override the preregistered decision>
```

## 17. Final decision template

```markdown
# Experiment 001 decision — <cohort-id>

Decision: <go | pivot | stop | no decision — rerun required>
Decision date (UTC):
Decision makers and conflicts:
Protocol and result report hashes:

## Rule application

- Study validity requirements met: <yes/no; evidence>
- Material-harm guardrails passed: <yes/no; evidence>
- Correctness non-inferiority passed: <yes/no; evidence>
- Efficiency outcomes meeting practical threshold: <list or none>
- Diagnostic-quality outcomes meeting practical threshold: <list or none>
- Scenario-consistency requirement passed: <yes/no; evidence>
- Exact preregistered rule selecting this decision: <quote rule identifier>

## Evidence-based conclusion

<what the experiment supports, bounded to the tested cohort>

## What the experiment does not establish

<causality limits, external-validity limits, implementation limits>

## Next action

For go: <authorized next-phase planning only; its gates still apply>
For pivot: <revised thesis, retained value, rejected claim, new experiment>
For stop: <work stopped and preserved evidence>
For no decision: <invalidity and preregistered rerun requirements>

## Deviations and dissent

<all deviations, minority assessment, or none>
```

## 18. Unresolved execution inputs

The following must be supplied and frozen before execution:

1. project/application, environment, topology, and traffic generator;
2. repository, deployment, runtime, and telemetry snapshot identifiers and hashes;
3. at least ten independent incident scenarios with independent truth packages;
4. telemetry sampling, coverage, freshness, clock-skew, and retention parameters;
5. agent model/version, inference settings, tools, and context limit;
6. time, query, byte, retry, and timeout budgets;
7. evaluator roster, adjudicator, conflicts, and training examples;
8. data owner, redaction profile, access list, retention period, and security contact;
9. cohort ID, randomization seed, replacement schedule, artifact location, and analysis script.

Until these inputs exist, Experiment 001 MUST NOT be executed and no thesis decision may be recorded.
