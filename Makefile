# watchmydog build entrypoints. This is the only file that knows about both
# the Go service and the web client.

MODULE   := github.com/maikomr/watchmydog
BIN      := bin/watchmydog
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -X $(MODULE)/internal/buildinfo.Version=$(VERSION) \
            -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

.PHONY: all build run test lint fmt web web-install web-dev web-test web-check clean check capture launcher install-capture go2rtc-dev secret serve serve-off install-agent uninstall-agent restart-agent agent-status logs doctor docker api-lint

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

## launcher: build WatchMyDog.app, the stable parent that holds camera/mic permission (ADR-0006)
launcher: bin/WatchMyDog.app/Contents/MacOS/watchmydog-launcher

bin/WatchMyDog.app/Contents/MacOS/watchmydog-launcher: launcher/main.swift launcher/Info.plist
	mkdir -p bin/WatchMyDog.app/Contents/MacOS
	swiftc -O -o $@ launcher/main.swift
	cp launcher/Info.plist bin/WatchMyDog.app/Contents/Info.plist
	codesign --force --sign - --identifier com.watchmydog.launcher bin/WatchMyDog.app

## install-capture: install the capture helper into ~/.local/bin (used by go2rtc)
install-capture: bin/wmd-capture
	install -m 755 bin/wmd-capture $(HOME)/.local/bin/wmd-capture

## go2rtc-dev: run go2rtc standalone with deploy/go2rtc.yaml (increment 1 proof)
go2rtc-dev: install-capture
	PATH="$(HOME)/.local/bin:/opt/homebrew/bin:$$PATH" go2rtc -config deploy/go2rtc.yaml

## secret: create the login secret in data/secret (once; delete the file to rotate)
secret:
	go run ./cmd/watchmydog gen-secret data/secret

## serve: publish the service on the tailnet over HTTPS (persists across reboots)
serve:
	tailscale serve --bg http://127.0.0.1:8080
	tailscale serve status

## serve-off: stop publishing the service on the tailnet
serve-off:
	tailscale serve --https=443 off

## install-agent: build and (re)install the launchd agent: starts at login, restarts on crash
install-agent:
	deploy/agent.sh install

## uninstall-agent: stop and remove the launchd agent
uninstall-agent:
	deploy/agent.sh uninstall

## restart-agent: restart the running service
restart-agent:
	deploy/agent.sh restart

## agent-status: show whether the agent is running
agent-status:
	deploy/agent.sh status

## logs: follow the service log
logs:
	tail -n 50 -f $(HOME)/Library/Logs/watchmydog/watchmydog.log

## doctor: check the Mac's settings and the service, and explain fixes
doctor:
	@deploy/doctor.sh

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

## docker: build the container image (cloud role, ADR-0011) for this machine's platform
docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t watchmydog:$(VERSION) .

## api-lint: check docs/openapi.yaml against the OpenAPI rules (downloads Redocly's CLI once)
api-lint:
	npx --yes @redocly/cli@1.25.0 lint docs/openapi.yaml
