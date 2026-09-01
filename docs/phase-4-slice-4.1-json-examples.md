# Phase 4 Slice 4.1 JSON examples

`prodmap baseline` uses the standard envelope and returns a query-time result.
The command never persists a baseline or claims causality.

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-28T12:01:00Z",
  "command": "baseline",
  "data": {
    "baseline_key": "sha256:0d7c13ee63c96d1bdca4f32b35c3e6d9629065ace8303645053032727852c97f",
    "status": "AVAILABLE",
    "method": "previous_window",
    "algorithm_version": "baseline-previous-window/v1-experimental",
    "environment": "reference",
    "target": {"kind": "service", "id": "01a049db-14e4-7798-983b-2946718efaa7", "service": "checkout"},
    "metric": "latency_p95",
    "unit": "nanoseconds",
    "at": "2026-08-28T12:00:00Z",
    "window": {"start": "2026-08-28T11:30:00Z", "end": "2026-08-28T12:00:00Z", "duration_seconds": 1800},
    "value": 21000000,
    "dispersion": null,
    "sample_count": 42,
    "reference_windows": 1,
    "coverage_ratio": 0.92,
    "is_complete": true,
    "accepted_windows": [{"id": "01a049db-14e4-7798-983b-2946718efaa7", "start": "2026-08-28T11:30:00Z", "end": "2026-08-28T12:00:00Z", "sample_count": 42}],
    "rejected_windows": [],
    "rejected_windows_count": 0,
    "confidence": {"level": "LOW", "basis": "single exact previous window; experimental algorithm caps confidence at LOW", "algorithm_version": "baseline-previous-window/v1-experimental", "limitations": []},
    "causality_claimed": false
  },
  "warnings": [],
  "pagination": null
}
```

If a window is missing, ambiguous, contaminated, insufficient, or future, the
same shape is returned with `status` and `confidence.level` equal to `UNKNOWN`,
`value: null`, and explicit limitations where applicable.

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-28T12:01:00Z",
  "command": "baseline",
  "data": {
    "baseline_key": "sha256:cf0d082573afb4d558b46d324dcc52344636452efdebb30a2364aa29ed4f7cae",
    "status": "UNKNOWN",
    "method": "previous_window",
    "algorithm_version": "baseline-previous-window/v1-experimental",
    "environment": "reference",
    "target": {"kind": "service", "id": "01a049db-14e4-7798-983b-2946718efaa7", "service": "checkout"},
    "metric": "latency_p95",
    "unit": "nanoseconds",
    "at": "2026-08-28T12:00:00Z",
    "window": {"start": "2026-08-28T11:30:00Z", "end": "2026-08-28T12:00:00Z", "duration_seconds": 1800},
    "value": null,
    "dispersion": null,
    "sample_count": 0,
    "reference_windows": 0,
    "coverage_ratio": null,
    "is_complete": false,
    "accepted_windows": [],
    "rejected_windows": [
      {
        "id": "01a049db-14e4-7798-983b-2946718efaa7",
        "start": "2026-08-28T11:30:00Z",
        "end": "2026-08-28T12:00:00Z",
        "observed_at": "2026-08-28T12:00:00Z",
        "sample_count": 4,
        "coverage_ratio": 0.9,
        "is_complete": true,
        "contaminated": false,
        "reason": "INSUFFICIENT_SAMPLES"
      }
    ],
    "rejected_windows_count": 1,
    "confidence": {"level": "UNKNOWN", "basis": "exact previous window has insufficient samples", "algorithm_version": "baseline-previous-window/v1-experimental", "limitations": ["sample count is below min_samples"]},
    "causality_claimed": false
  },
  "warnings": [],
  "pagination": null
}
```
