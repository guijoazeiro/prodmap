# Phase 3 Slice 3.2 JSON examples

`deploys` now includes `runtime_evidence_until` and a derived,
non-causal `runtime_association`. A compatible immutable artifact produces
`MATCHED/HIGH`; a mixed fleet produces `PARTIAL/MEDIUM`; absent runtime or
concurrent deployment produces `UNKNOWN`; immutable mismatch produces
`CONTRADICTED/UNKNOWN`.

```json
{"runtime_evidence_until":"2026-08-26T13:00:00Z","items":[{"runtime_association":{"status":"MATCHED","relation_type":"INFERRED","confidence":{"level":"HIGH","basis":"immutable artifact identity observed in compatible runtime window"},"algorithm_version":"deployment-runtime/v1","window":{"start":"2026-08-26T12:00:00Z","end":"2026-08-26T12:30:00Z","end_reason":"max_confirmation_delay"},"candidate_instances":1,"matched_instances":1,"contradicted_instances":0,"runtime_instances":[],"evidence":[],"limitations":[],"causality_claimed":false}}]}
```
