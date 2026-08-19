.PHONY: build dev run test

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
