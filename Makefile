.PHONY: build dev run test test-integration

VERSION ?= dev
COMMIT ?= unknown
BUILD_DATE ?= unknown
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o ./bin/prodmap ./cmd/prodmap

dev:
	go tool air -c .air.toml

run:
	go run ./cmd/prodmap

test:
	go test ./...

test-integration:
	PRODMAP_TEST_REAL_DOCKER=1 go test ./internal/docker -run '^TestRealDockerInspection$$' -count=1 -v
	PRODMAP_TEST_REAL_OTEL=1 go test ./internal/cli -run '^TestRealOTelCollector$$' -count=1 -v
