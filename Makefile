# watchmydog build entrypoints. This is the only file that knows about both
# the Go service and the web client.

MODULE   := github.com/maikomr/watchmydog
BIN      := bin/watchmydog
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -X $(MODULE)/internal/buildinfo.Version=$(VERSION) \
            -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

.PHONY: all build run test lint fmt web web-install web-dev clean check

all: check build

## build: compile the Go service into bin/
build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/watchmydog

## run: run the Go service from source (Ctrl-C to stop)
run:
	go run -ldflags "$(LDFLAGS)" ./cmd/watchmydog

## test: run Go unit tests with the race detector
test:
	go test -race -cover ./...

## lint: static checks (go vet now; staticcheck/golangci-lint later)
lint:
	go vet ./...

## fmt: format Go code
fmt:
	gofmt -l -w cmd internal

## check: everything CI runs
check: fmt lint test

## web-install: install web client dependencies (uses the lockfile)
web-install:
	cd web && npm ci

## web: type-check and build the web client into web/dist
web:
	cd web && npm run build

## web-dev: run the Vite dev server with API proxy to :8080
web-dev:
	cd web && npm run dev

## clean: remove build outputs
clean:
	rm -rf bin web/dist

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
