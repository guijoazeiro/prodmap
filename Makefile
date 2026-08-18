.PHONY: build dev run test

build:
	go build -o ./bin/prodmap ./cmd/prodmap

dev:
	go tool air -c .air.toml

run:
	go run ./cmd/prodmap

test:
	go test ./...
