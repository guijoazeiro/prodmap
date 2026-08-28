# Prodmap

Production context for coding agents.

## Requirements

- Go 1.26.5 or newer in the 1.26 line
- GNU Make (optional, used for the documented shortcuts)

## Local development

The project uses [Air](https://github.com/air-verse/air) for live reload. Its version is pinned in `go.mod`, so a separate global installation is not required.

Start the development watcher from the repository root:

```bash
make dev
```

Air builds `./cmd/prodmap`, starts the generated binary, and rebuilds/restarts it after relevant source or configuration changes. Build artifacts are written to `.tmp/` and removed when the watcher exits.

Useful commands:

```bash
make run    # run once without live reload
make build  # build ./bin/prodmap
make test   # run the test suite
```

Live reload is a local development facility only. It is not part of the production runtime or deployment model.

## Foundation CLI

Build and inspect the development binary:

```bash
make build
./bin/prodmap version
./bin/prodmap version --json
```

Initialize a project without contacting external services:

```bash
./bin/prodmap init
./bin/prodmap doctor
./bin/prodmap doctor --json
```

`init` creates `.prodmap/config.yaml` only when it is absent, prepares `.prodmap/prodmap.db`, and applies the embedded migrations. It is safe to run repeatedly and never overwrites the configuration or removes the database.

Configuration precedence is flags, `PRODMAP_*` environment variables, project configuration, user configuration, and compiled defaults. The project file is `.prodmap/config.yaml`; the user file follows `$XDG_CONFIG_HOME/prodmap/config.yaml`. Supported environment variables are `PRODMAP_PROJECT_DIR`, `PRODMAP_DATA_DIR`, `PRODMAP_LOG_LEVEL`, and `PRODMAP_LOG_FORMAT`. Configuration files must use `schema_version: "1.0"`; unknown fields are rejected.

`doctor` checks the local configuration, project and data directories, SQLite migrations, optional Git and Docker availability, and the development hot-reload files. Missing optional tools are warnings during Foundation.

## Runtime provenance prototype

Phase 1 adds a local, read-only Docker-to-Git provenance slice:

```bash
./bin/prodmap status
./bin/prodmap services
./bin/prodmap runtime
./bin/prodmap runtime --refresh
./bin/prodmap explain <runtime-or-artifact-or-commit-or-correlation-id>
```

Each command also accepts `--json`. `runtime` supports `--service`, `--environment`, `--at`, `--limit`, and `--cursor`; `explain` supports `--detail summary|full` and `--at`. A refresh uses only local, read-only `docker ps`, container/image inspection, and Git metadata commands. It never pulls images, contacts Git remotes, checks out commits, or mutates containers. Only the OCI `title`, `revision`, `source`, and `created` annotations cross the Docker boundary; `version`, raw inspect payloads, environment variables, mounts, health logs, and all other labels are discarded.

The prototype derives a service logical key from the normalized image repository name, not from the container name. Artifact identity prefers a validated `sha256` or `sha512` repository digest, then a validated immutable local image ID, and only then a mutable tag. OCI revision metadata can produce an `EXACT` correlation only when the artifact identity is immutable, the complete SHA resolves locally, and no identity or temporal contradiction exists. Missing or ambiguous data remains `LOW` or `UNKNOWN` and is retained in the evidence explanation.

Runtime disappearance is not interpreted as removal and freshness uses a provisional five-minute local threshold. Without an explicit refresh environment, runtime inventory uses `default`.

## Phase 2A experimental validation slice

An explicitly authorized, bounded exception prepares the topology input for Experiment 001 without declaring Phase 2 complete or deciding `go`, `pivot`, or `stop`. It accepts only frozen OTLP trace JSONL and produces observed topology plus temporal aggregates:

```bash
./bin/prodmap telemetry ingest \
  --file testdata/otel/linked-services.otlp.jsonl \
  --window-start 2026-08-19T12:00:00Z \
  --window-end 2026-08-19T12:01:00Z \
  --json

./bin/prodmap graph \
  --service checkout \
  --at 2026-08-19T12:00:02.5Z \
  --json
```

The input is one official OTLP `ExportTraceServiceRequest` JSON message per line. Parsing and redaction finish before a database transaction begins. Every edge is `OBSERVED`, never `EXACT`; missing destination data creates no edge. The optional pinned Collector starter and shutdown instructions are in [`deploy/otel-collector`](deploy/otel-collector/README.md). A manual smoke test is documented in [`docs/phase-2a-smoke-test.md`](docs/phase-2a-smoke-test.md).

The versioned JSON contract examples are in [`docs/phase-2a-json-examples.md`](docs/phase-2a-json-examples.md).

## Runtime and observed-service evidence

Use the same environment for Docker inventory and frozen telemetry:

```bash
./bin/prodmap runtime --refresh --environment reference
./bin/prodmap telemetry ingest --file traces.otlp.jsonl --environment reference \
  --window-start 2026-08-19T12:00:00Z --window-end 2026-08-19T12:01:00Z
./bin/prodmap services --json
```

`services` reports `telemetry_observed` and `runtime_association`. An association is `MATCHED/HIGH` only when the allowlisted OCI image title and normalized OTel service identity match exactly in the same environment. The title is not secret, but arbitrary labels remain blocked. Without a title, association is `UNKNOWN`; image references, container names, and fuzzy matching are not evidence. Runtime association does not establish causality and is never `EXACT`.

## Temporal endpoint context

`endpoints` reads materialized endpoint telemetry at a specific instant:

```bash
./bin/prodmap endpoints --service checkout-api --environment reference --at 2026-08-24T13:40:30Z --json
```

Only windows active in `[window_start, window_end)` are returned. Overlapping windows remain separate and are never summed; no active window is not evidence that traffic or an endpoint is absent.

## Phase 3 Slice 3.1 — offline deployment ledger

Phase 3 is authorized for limited learning. Slice 3.1 ingests a frozen deployment
ledger atomically and exposes registered deployments without claiming a runtime
match, causality, baseline, or regression:

```bash
./bin/prodmap deployments ingest --file deployments.jsonl --json
./bin/prodmap deploys --environment reference --json
```

The input contract is [deployment-ledger-jsonl/v1](docs/contracts/deployment-ledger-jsonl-v1.md).
Replay is idempotent, append-only ledgers may add records, and changed deployment
identities conflict rather than overwrite history. The slice has no Build entity,
timeline or remote deployment source.

Slice 3.2 derives deployment/runtime association during `deploys` queries from
immutable runtime identity in a bounded confirmation window; it remains inferred,
non-causal, and is never persisted as a deployment conclusion.

Slice 3.3 adds `prodmap timeline`, a dynamic, paginated merge of declared
deployment events and observed runtime events. Declared rollbacks and concurrent
deployments remain visible but never imply a causal runtime effect.

Slice 3.4A delivered the bounded GitHub Actions HTTP artifact boundary. Slice
3.4B1 delivers the explicit CLI and offline atomic persistence of a verified
`deployment-ledger-jsonl/v1` artifact without treating workflow runs as
deployments. Publication and a real GitHub smoke remain reserved for Slice
3.4B2.

Phase 4 Slice 4.1 adds `prodmap baseline`: a read-only, exact prior-window
reference for one service or endpoint metric. It reports `AVAILABLE/LOW` only
for one eligible `[at-window,at)` telemetry window and otherwise reports
`UNKNOWN`; it does not persist a baseline, detect regression, or claim
causality. Phase 5 remains blocked.

Metrics/logs ingestion, a live receiver in Prodmap, regression classification,
generic export, MCP, and causal scoring remain out of scope. Experiment 001 has
not been executed by this implementation.

## Specifications

- [`docs/prodmap-product-spec-v2.md`](docs/prodmap-product-spec-v2.md)
- [`docs/prodmap-technical-spec-v1.md`](docs/prodmap-technical-spec-v1.md)
- [`docs/experiments/001-correlation-vs-raw-context.md`](docs/experiments/001-correlation-vs-raw-context.md)
