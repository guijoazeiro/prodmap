# Phase 3 Slice 3.1 JSON examples

Ingest a frozen ledger:

```bash
prodmap deployments ingest --file deployments.jsonl --json
```

```json
{"schema_version":"1.0","command":"deployments ingest","data":{"ingestion_id":"<uuidv7>","source_hash":"sha256:<hex>","format":"deployment-ledger-jsonl/v1","records_seen":2,"deployments_inserted":2,"deployments_existing":0,"idempotent_replay":false},"warnings":[],"pagination":null}
```

Query registered deployments in a half-open interval:

```bash
prodmap deploys --environment reference --since 2026-08-26T11:00:00Z --until 2026-08-26T13:00:00Z --json
```

The response contains `items: []`, a versioned envelope, and keyset pagination.
Each item has a registered status and non-causal provenance. `MATCHED/HIGH` means
only that an existing immutable artifact and verified commit resolved without
contradiction; it does not claim a runtime match, causality, or `EXACT`.
