# Phase 4 Slice 4.2 JSON examples

`prodmap regression` compares exact service-level windows around one deployment.
It reports mechanical comparability only and never classifies a regression.

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-31T15:00:00Z",
  "command": "regression",
  "data": {
    "comparison_key": "sha256:8f56e342acdd6c63f9cae310b910b5a16857307b9e737b30eae2d703d03c0e43",
    "status": "AVAILABLE",
    "algorithm_version": "deployment-comparison/v1-experimental",
    "deployment": {"id": "01a049db-14e4-7798-983b-2946718efaa7", "external_id": "payment-latency-v1", "environment": "reference", "service": "payment-api", "started_at": "2026-08-31T14:00:00Z"},
    "metric": "latency_p95",
    "unit": "nanoseconds",
    "before": {"status": "AVAILABLE", "algorithm_version": "baseline-previous-window/v1-experimental", "window": {"start": "2026-08-31T13:30:00Z", "end": "2026-08-31T14:00:00Z"}, "value": 20000000, "sample_count": 20, "coverage_ratio": 1, "is_complete": true, "accepted_windows": [{"id": "01a049d8-8d89-7765-b8d5-9e5a0983f4e3", "start": "2026-08-31T13:30:00Z", "end": "2026-08-31T14:00:00Z", "sample_count": 20}], "rejected_windows": []},
    "after": {"status": "AVAILABLE", "algorithm_version": "observation-exact-window/v1-experimental", "window": {"start": "2026-08-31T14:00:00Z", "end": "2026-08-31T14:30:00Z"}, "value": 770000000, "sample_count": 20, "coverage_ratio": 1, "is_complete": true, "accepted_windows": [{"id": "01a049d8-8d89-7766-b8d5-9e5a0983f4e4", "start": "2026-08-31T14:00:00Z", "end": "2026-08-31T14:30:00Z", "sample_count": 20}], "rejected_windows": []},
    "absolute_delta": 750000000,
    "relative_delta": 37.5,
    "contamination": {"before_deployments": [], "after_deployments": [], "concurrent_deployments": [], "truncated": false},
    "baseline_confidence": {"level": "LOW", "basis": "single exact baseline window; experimental algorithm caps confidence at LOW", "algorithm_version": "baseline-previous-window/v1-experimental", "limitations": ["baseline: known deployment contamination checks only registered deployments started within [window_start, window_end); absence of a registered deployment does not establish absence of change, rollout, configuration, incident, or prior residual effect", "comparison uses only two exact windows", "no historical or seasonal baseline is available", "percentile metrics are sensitive to the observed sample set", "known deployment contamination is limited to registered deployments", "temporal proximity does not establish causality"]},
    "observation_confidence": {"level": "LOW", "basis": "single exact post-deployment observation window; experimental algorithm caps confidence at LOW", "algorithm_version": "observation-exact-window/v1-experimental", "limitations": ["observation: known deployment contamination checks only registered deployments started within [window_start, window_end); absence of a registered deployment does not establish absence of change, rollout, configuration, incident, or prior residual effect", "comparison uses only two exact windows", "no historical or seasonal baseline is available", "percentile metrics are sensitive to the observed sample set", "known deployment contamination is limited to registered deployments", "temporal proximity does not establish causality"]},
    "regression_confidence": {"level": "LOW", "basis": "two exact windows are mechanically comparable; experimental algorithm caps confidence at LOW", "algorithm_version": "deployment-comparison/v1-experimental", "limitations": ["comparison uses only two exact windows", "no historical or seasonal baseline is available", "percentile metrics are sensitive to the observed sample set", "known deployment contamination is limited to registered deployments", "temporal proximity does not establish causality"]},
    "classification": null,
    "causality_claimed": false
  },
  "warnings": [],
  "pagination": null
}
```

Exemplo de `UNKNOWN` quando o fim da observação posterior ainda está no futuro:

```json
{"schema_version":"1.0","generated_at":"2026-08-31T14:10:00Z","command":"regression","data":{"comparison_key":"sha256:3a7d2b0c4a75fd2f1794a2e28bf6a6f0f09c3cb4b17e7b7b06cf7b5e73f9dc1e","status":"UNKNOWN","algorithm_version":"deployment-comparison/v1-experimental","deployment":{"id":"01a049db-14e4-7798-983b-2946718efaa7","external_id":"payment-latency-v1","environment":"reference","service":"payment-api","started_at":"2026-08-31T14:00:00Z"},"metric":"latency_p95","unit":"nanoseconds","before":{"status":"AVAILABLE","algorithm_version":"baseline-previous-window/v1-experimental","window":{"start":"2026-08-31T13:30:00Z","end":"2026-08-31T14:00:00Z"},"value":20000000,"sample_count":20,"coverage_ratio":1,"is_complete":true,"accepted_windows":[{"id":"01a049d8-8d89-7765-b8d5-9e5a0983f4e3","start":"2026-08-31T13:30:00Z","end":"2026-08-31T14:00:00Z","sample_count":20}],"rejected_windows":[]},"after":{"status":"UNKNOWN","algorithm_version":"observation-exact-window/v1-experimental","window":{"start":"2026-08-31T14:00:00Z","end":"2026-08-31T14:30:00Z"},"value":null,"sample_count":0,"coverage_ratio":null,"is_complete":false,"accepted_windows":[],"rejected_windows":[]},"baseline_confidence":{"level":"LOW","basis":"single exact baseline window; experimental algorithm caps confidence at LOW","algorithm_version":"baseline-previous-window/v1-experimental","limitations":["comparison uses only two exact windows"]},"observation_confidence":{"level":"UNKNOWN","basis":"post-deployment observation interval is in the future relative to generated_at","algorithm_version":"observation-exact-window/v1-experimental","limitations":["post-deployment observation interval is in the future relative to generated_at"]},"regression_confidence":{"level":"UNKNOWN","basis":"comparison requires eligible exact baseline and observation windows without known contamination or concurrency","algorithm_version":"deployment-comparison/v1-experimental","limitations":["comparison requires eligible exact baseline and observation windows without known contamination or concurrency","post-deployment interval is in the future relative to generated_at"]},"classification":null,"causality_claimed":false},"warnings":[],"pagination":null}
```
