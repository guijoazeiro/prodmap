# Phase 2A manual smoke test

Build and ingest the versioned fixture:

```bash
make build
SMOKE_DIR="$(mktemp -d)"
bin/prodmap telemetry ingest --file testdata/otel/linked-services.otlp.jsonl \
  --window-start 2026-08-19T12:00:00Z --window-end 2026-08-19T12:01:00Z \
  --project-dir "$SMOKE_DIR" --json
bin/prodmap graph --service checkout --at 2026-08-19T12:00:02.5Z \
  --project-dir "$SMOKE_DIR" --json
```

For fish, use `set SMOKE_DIR (mktemp -d)`. Re-run the ingest and verify the same `ingestion_id` with `idempotent_replay: true`. Query before the window and with `--min-confidence exact`; both should return no edges.

For a real Collector smoke test, follow [`deploy/otel-collector/README.md`](../deploy/otel-collector/README.md), stop the Collector to freeze the JSONL file, then ingest it. The opt-in automated check is:

```bash
PRODMAP_TEST_REAL_OTEL=1 go test ./internal/cli -run TestRealOTelCollector -count=1 -v
```

Do not interpret this smoke test as execution of Experiment 001 or a Phase 2 `go` decision.
