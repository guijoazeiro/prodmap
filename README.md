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

Each command also accepts `--json`. `runtime` supports `--service`, `--environment`, `--at`, `--limit`, and `--cursor`; `explain` supports `--detail summary|full` and `--at`. A refresh uses only local, read-only `docker ps`, container/image inspection, and Git metadata commands. It never pulls images, contacts Git remotes, checks out commits, or mutates containers. Only the OCI `revision`, `source`, `version`, and `created` annotations cross the Docker boundary; raw inspect payloads, environment variables, mounts, health logs, and all other labels are discarded.

The prototype derives a service logical key from the normalized image repository name, not from the container name. Artifact identity prefers a repository digest, then the immutable local image ID, and only then a mutable tag. OCI revision metadata can produce an `EXACT` correlation only when the artifact identity is immutable and the complete SHA resolves locally without contradiction. Missing or ambiguous data remains `LOW` or `UNKNOWN` and is retained in the evidence explanation.

Phase 1 deliberately has no OpenTelemetry, deployment intelligence, production graph, Kubernetes, or remote source integrations. Runtime disappearance is not interpreted as removal, freshness uses a provisional five-minute local threshold, and the `default` environment is used until environment mapping is introduced in a later phase. This prototype does not constitute execution or a `go`, `pivot`, or `stop` decision for Experiment 001.

## Specifications

- [`docs/prodmap-product-spec-v2.md`](docs/prodmap-product-spec-v2.md)
- [`docs/prodmap-technical-spec-v1.md`](docs/prodmap-technical-spec-v1.md)
