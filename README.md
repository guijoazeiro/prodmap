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

## Specifications

- [`docs/prodmap-product-spec-v2.md`](docs/prodmap-product-spec-v2.md)
- [`docs/prodmap-technical-spec-v1.md`](docs/prodmap-technical-spec-v1.md)
