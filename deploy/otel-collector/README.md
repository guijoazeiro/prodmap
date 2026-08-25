# Optional Phase 2A OTel Collector

This isolated starter accepts OTLP traces on localhost and appends canonical OTLP/JSON records to `data/traces.otlp.jsonl`. It has no backend or credentials and does not collect metrics or logs. The pinned `health-probe` sidecar makes `docker compose up --wait` verify the Collector health extension over HTTP on `:13133`; it does not treat a binary version check as readiness.

```bash
install -d -m 0700 deploy/otel-collector/data
OTEL_UID=$(id -u) OTEL_GID=$(id -g) docker compose -f deploy/otel-collector/compose.yaml up -d --wait
# send OTLP traces to localhost:4317 or localhost:4318
docker compose -f deploy/otel-collector/compose.yaml stop
docker compose -f deploy/otel-collector/compose.yaml down
```

The `:Z` bind-mount option supports Fedora/SELinux. The container runs with the invoking user's UID/GID, so the raw trace directory remains private. Raw OTLP may contain credentials or personal data even though Prodmap discards those fields; protect and delete the source snapshot accordingly. Generated data is ignored by Git. Stop the Collector before ingesting so the file is a frozen snapshot:

```bash
bin/prodmap telemetry ingest \
  --file deploy/otel-collector/data/traces.otlp.jsonl \
  --window-start 2026-08-19T12:00:00Z \
  --window-end 2026-08-19T12:05:00Z
```

Remove only generated output when it is no longer needed:

```bash
rm deploy/otel-collector/data/traces.otlp.jsonl
```
