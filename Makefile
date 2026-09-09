.PHONY: build dev run test test-integration test-mcp-agent test-scripts

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

test-scripts:
	bash -n scripts/test-mcp-agent.sh scripts/test/mcp_agent_test.sh
	./scripts/test/mcp_agent_test.sh

test-integration:
	PRODMAP_TEST_REAL_DOCKER=1 go test ./internal/docker -run '^TestRealDockerInspection$$' -count=1 -v
	PRODMAP_TEST_REAL_OTEL=1 go test ./internal/cli -run '^TestRealOTelCollector$$' -count=1 -v

# Opt-in: uses the locally authenticated Codex CLI and may consume model usage.
test-mcp-agent: build
	MCP_AGENT_PRODMAP_BIN="$(CURDIR)/bin/prodmap" ./scripts/test-mcp-agent.sh
