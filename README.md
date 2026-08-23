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

Each command also accepts `--json`. `runtime` supports `--service`, `--environment`, `--at`, `--limit`, and `--cursor`; `explain` supports `--detail summary|full` and `--at`. A refresh uses only local, read-only `docker ps`, container/image inspection, and Git metadata commands. It never pulls images, contacts Git remotes, checks out commits, or mutates containers. Only the OCI `revision`, `source`, and `created` annotations cross the Docker boundary; `version`, raw inspect payloads, environment variables, mounts, health logs, and all other labels are discarded.

The prototype derives a service logical key from the normalized image repository name, not from the container name. Artifact identity prefers a validated `sha256` or `sha512` repository digest, then a validated immutable local image ID, and only then a mutable tag. OCI revision metadata can produce an `EXACT` correlation only when the artifact identity is immutable, the complete SHA resolves locally, and no identity or temporal contradiction exists. Missing or ambiguous data remains `LOW` or `UNKNOWN` and is retained in the evidence explanation.

Phase 1 deliberately has no OpenTelemetry, deployment intelligence, production graph, Kubernetes, or remote source integrations. Runtime disappearance is not interpreted as removal, freshness uses a provisional five-minute local threshold, and the `default` environment is used until environment mapping is introduced in a later phase.

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

Metrics/logs ingestion, a live receiver in Prodmap, deployments, baselines, regressions, generic export, MCP, and causal scoring remain out of scope. Experiment 001 has not been executed by this implementation.

## Specifications

- [`docs/prodmap-product-spec-v2.md`](docs/prodmap-product-spec-v2.md)
- [`docs/prodmap-technical-spec-v1.md`](docs/prodmap-technical-spec-v1.md)
- [`docs/experiments/001-correlation-vs-raw-context.md`](docs/experiments/001-correlation-vs-raw-context.md)
