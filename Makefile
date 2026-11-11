# watchmydog build entrypoints. This is the only file that knows about both
# the Go service and the web client.

MODULE   := github.com/maikomr/watchmydog
BIN      := bin/watchmydog
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -X $(MODULE)/internal/buildinfo.Version=$(VERSION) \
            -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

.PHONY: all build run test lint fmt web web-install web-dev web-test web-check clean check capture install-capture go2rtc-dev

all: check build

## build: compile the Go service into bin/
build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/watchmydog

CONFIG ?= deploy/watchmydog.local.yaml

## run: build the web client, then run the Go service with $(CONFIG) (Ctrl-C to stop)
run: install-capture web
	@test -f $(CONFIG) || cp deploy/watchmydog.example.yaml $(CONFIG)
	PATH="$(HOME)/.local/bin:/opt/homebrew/bin:$$PATH" go run -ldflags "$(LDFLAGS)" ./cmd/watchmydog -config $(CONFIG)

## test: run Go unit tests with the race detector
test:
	go test -race -cover ./...

## lint: static checks (go vet now; staticcheck/golangci-lint later)
lint:
	go vet ./...

## fmt: format Go code
fmt:
	gofmt -l -w cmd internal

## check: everything CI runs (Go and web)
check: fmt lint test web-check

## capture: build the native macOS capture helper into bin/wmd-capture
capture: bin/wmd-capture

bin/wmd-capture: capture/wmd-capture/main.swift
	mkdir -p bin
	swiftc -O -o $@ $<

## install-capture: install the capture helper into ~/.local/bin (used by go2rtc)
install-capture: bin/wmd-capture
	install -m 755 bin/wmd-capture $(HOME)/.local/bin/wmd-capture

## go2rtc-dev: run go2rtc standalone with deploy/go2rtc.yaml (increment 1 proof)
go2rtc-dev: install-capture
	PATH="$(HOME)/.local/bin:/opt/homebrew/bin:$$PATH" go2rtc -config deploy/go2rtc.yaml

## web-install: install web client dependencies (uses the lockfile)
web-install:
	cd web && npm ci

## web: type-check and build the web client into web/dist
web:
	cd web && npm run build

## web-dev: run the Vite dev server with API proxy to :8080
web-dev:
	cd web && npm run dev

## web-test: run web unit tests (vitest)
web-test:
	cd web && npm test

## web-check: type-check and test the web client
web-check:
	cd web && npm run typecheck && npm test

## clean: remove build outputs
clean:
	rm -rf bin web/dist

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
