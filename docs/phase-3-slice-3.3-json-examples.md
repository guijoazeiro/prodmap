# Phase 3 Slice 3.3 JSON examples

The `timeline` command is a dynamic, non-causal read model. `items`, `limitations`, and `warnings` are always arrays; an absent next page is represented by `next_cursor: null`.

```json
{
  "schema_version": "1.0",
  "generated_at": "2026-08-26T12:30:00Z",
  "command": "timeline",
  "data": {
    "since": "2026-08-26T12:00:00Z",
    "until": "2026-08-26T14:00:00Z",
    "environment": "reference",
    "items": [
      {
        "id": "evt_deployment_018fc2a0-0000-7000-8000-000000000001",
        "time": "2026-08-26T12:00:00Z",
        "kind": "deployment_running",
        "environment": "reference",
        "service": "checkout-api",
        "relation_type": "DECLARED",
        "subject": {
          "type": "deployment",
          "id": "018fc2a0-0000-7000-8000-000000000001",
          "name": "reference-checkout-api-20260826120000"
        },
        "source": {
          "kind": "deployment_ledger",
          "observed_at": "2026-08-26T12:01:00Z",
          "freshness_seconds": 1740
        },
        "confidence": {
          "level": "HIGH",
          "basis": "validated deployment ledger record"
        },
        "deployment": {
          "status": "running",
          "strategy": "compose",
          "provenance_status": "EXACT",
          "provenance_confidence": {
            "level": "HIGH",
            "basis": "immutable artifact and verified revision"
          }
        },
        "runtime": null,
        "concurrency": { "detected": false, "count": 1 },
        "limitations": [],
        "causality_claimed": false
      },
      {
        "id": "evt_runtime_018fc2a1-0000-7000-8000-000000000002",
        "time": "2026-08-26T12:00:05Z",
        "kind": "runtime_observed",
        "environment": "reference",
        "service": "checkout-api",
        "relation_type": "OBSERVED",
        "subject": {
          "type": "runtime_instance",
          "id": "018fc2a1-0000-7000-8000-000000000002",
          "name": "checkout-api"
        },
        "source": {
          "kind": "docker",
          "observed_at": "2026-08-26T12:00:05Z",
          "freshness_seconds": 1795
        },
        "confidence": {
          "level": "HIGH",
          "basis": "validated Docker runtime snapshot"
        },
        "deployment": null,
        "runtime": {
          "state": "running",
          "health": "healthy",
          "restart_count": 0,
          "artifact_identity": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        },
        "concurrency": { "detected": false, "count": 0 },
        "limitations": [],
        "causality_claimed": false
      }
    ]
  },
  "warnings": [],
  "pagination": {
    "limit": 100,
    "next_cursor": null
  }
}
```
