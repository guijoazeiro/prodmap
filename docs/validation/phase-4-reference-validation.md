# Phase 4 Slice 4.4 Reference Validation

Prodmap HEAD: `c4d9dababaf52bfe3d292396198c708c01102cde` (with the local
service-level-window correction under validation).

Application evidence HEAD: `5b297da`.

Evidence exporter HEAD: `f9c6133`.

Package integrity passed with `sha256sum -c SHA256SUMS`:

- `healthy.deployments.jsonl`: `3782947431d96373733d1964b19cd62276e466466f88dfa30c170a12e81f47ef`
- `healthy.traces.otlp.jsonl`: `c30c60d960b81f44d84d9886696844f386489ccfc92a1d261ffb62e7e3a31f13`
- `healthy.trafficgen.json`: `bbac80b7839e6f83eca056178229239e7695cc64be0dcb9965420185dccd9102`
- `payment-latency-v1.deployments.jsonl`: `adc29c4d781e4292b1b0b63fd266a3c195a665ea7579aeab673a65070cdaa0b9`
- `payment-latency-v1.traces.otlp.jsonl`: `878f9ad698aefad27dd95550e52411d7f49edc96bdb60bfd7e84b31c0535582b`
- `payment-latency-v1.trafficgen.json`: `af37b35c3756f3d2a0affae051b80efeddf85840907e2aba2412b1cfe2b542cc`
- `manifest.json`: `d7f419c01d33f33f7246e4616b4a4ead6e2c84267b111b8d90fdd8e1ef094432`

Essential validation commands used fresh temporary projects:

```bash
make build
prodmap deployments ingest --file <ledger> --project-dir "$PROJECT" --json
prodmap telemetry ingest --file <traces> --environment reference \
  --window-start <RFC3339> --window-end <RFC3339> --project-dir "$PROJECT" --json
prodmap regression --deployment <payment-deployment-id> --metric latency_p95 \
  --before 5m --after 5m --min-samples <N> --project-dir "$PROJECT" --json
```

## Corrected blocker

The initial failure was confirmed and corrected: SERVER spans that had an
endpoint were materialized only as endpoint windows, while baseline and
deployment comparison correctly query service windows (`endpoint_id IS NULL`).
The decoder now materializes both service and endpoint windows from the same
raw SERVER spans. The corrected ingests each report four telemetry windows:
one service and one endpoint window for `checkout-api`, and the same pair for
`payment-api`.

## Results

- Healthy: `UNKNOWN` / classification `UNKNOWN`; it never produced
  `CANDIDATE`. Its missing eligible prior window remains insufficiency, not a
  health conclusion.
- payment-latency-v1 with `--min-samples 8`: correctly returned `UNKNOWN`
  because the exact post-deployment `payment-api` service window has four
  SERVER spans, with rejected-window reason `INSUFFICIENT_SAMPLES`. This
  demonstrates protection against insufficient evidence.
- With the contract-valid `--min-samples 4`, the same uncontaminated exact
  windows produce
  `AVAILABLE`, `CANDIDATE`, `LOW`, `INCREASE`, absolute delta `750708783` ns,
  and relative delta `5542.126780111476`. This establishes the Slice 4.4
  technical flow without modifying evidence or timestamps.

Replay at the diagnostic threshold preserved both `comparison_key` and
`classification_key`. The half-open boundary check returned one endpoint at
`2026-09-01T13:27:15Z` and none at `2026-09-01T13:32:15Z`.

The inspected regression and endpoint JSON did not contain bearer tokens,
authorization data, local paths, image references, or the reference Git SHA.
Outputs contained no `HIGH`, `EXACT`, or `CONFIRMED`; all classifications kept
`causality_claimed: false`.

## Limitation and conclusion

Slice 4.4 technical validation: **PASS WITH LIMITATIONS**.

Four samples exercise the technical flow but do not validate statistical
precision or calibrate the experimental threshold. The product direction and
thesis remain **INCONCLUSIVE**, causality is not established, and Phase 5
remains blocked.
