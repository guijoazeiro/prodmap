# Phase 2A JSON examples

Identifiers and hashes below are illustrative; command envelopes always include `generated_at`, `warnings`, and `pagination` according to schema `1.0`.

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-19T12:01:00Z",
  "command": "telemetry ingest",
  "data": {
    "ingestion_id": "0198c433-8de0-7000-8000-000000000001",
    "source_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "environment": "default",
    "window_start": "2026-08-19T12:00:00Z",
    "window_end": "2026-08-19T12:01:00Z",
    "lines": 1,
    "resource_spans": 2,
    "spans_seen": 3,
    "spans_accepted": 3,
    "spans_ignored": 0,
    "services": 2,
    "endpoints": 2,
    "dependencies": 1,
    "observations": 1,
    "telemetry_windows": 2,
    "idempotent_replay": false
  },
  "warnings": [],
  "pagination": null
}
```

`graph` returns service/dependency nodes and temporal `OBSERVED` edges. Each edge carries its rule-based confidence, `otel-topology/v1`, evidence UUIDs, and limitations:

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-19T12:01:00Z",
  "command": "graph",
  "data": {
    "at": "2026-08-19T12:00:02.5Z",
    "environment": "default",
    "roots": ["0198c433-8de0-7000-8000-000000000001"],
    "nodes": [
      {
        "id": "0198c433-8de0-7000-8000-000000000001",
        "type": "service",
        "logical_key": "checkout",
        "display_name": "checkout"
      },
      {
        "id": "0198c433-8de0-7000-8000-000000000002",
        "type": "service",
        "logical_key": "payment",
        "display_name": "payment"
      }
    ],
    "edges": [
      {
        "id": "0198c433-8de0-7000-8000-000000000003",
        "from": "0198c433-8de0-7000-8000-000000000001",
        "to": "0198c433-8de0-7000-8000-000000000002",
        "relation_type": "OBSERVED",
        "dependency_kind": "service",
        "window_start": "2026-08-19T12:00:00Z",
        "window_end": "2026-08-19T12:01:00Z",
        "request_count": 1,
        "error_count": 1,
        "duration_sum_ns": 1000000,
        "confidence": {
          "level": "HIGH",
          "basis": "parent/child client/server propagation",
          "algorithm_version": "otel-topology/v1"
        },
        "evidence_ids": [
          "0198c433-8de0-7000-8000-000000000004",
          "0198c433-8de0-7000-8000-000000000005"
        ],
        "limitations": []
      }
    ],
    "truncated": false
  },
  "warnings": [],
  "pagination": null
}
```

Raw trace/span IDs and attributes are never returned. Selecting `--min-confidence exact` produces no OTel edges because observed topology is not exact identity or causality.

For a direct synchronous CLIENT→SERVER relation, each pair contributes one request and one error when either side reports `Status=ERROR` or an HTTP response status of 500 or higher. For asynchronous traffic, a PRODUCER with a CONSUMER `SpanLink` uses basis `producer/consumer SpanLink association` and retains outbound error semantics. Semantic conventions without a direct association remain a MEDIUM fallback. If a direct target contradicts `peer.service` or `rpc.service`, the direct target is retained, confidence is capped at MEDIUM, and the contradiction appears in `limitations`. Resolved service targets are stored per observation, so a later window never rewrites an earlier dependency node.
