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

## Specifications

- [`docs/prodmap-product-spec-v2.md`](docs/prodmap-product-spec-v2.md)
- [`docs/prodmap-technical-spec-v1.md`](docs/prodmap-technical-spec-v1.md)
