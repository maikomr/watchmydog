# watchmydog

Watch your dog from your phone while you are away. A Go service on a Mac mini streams the USB webcam's
video **and audio** over WebRTC to an Android phone through a Tailscale VPN. Later increments add
howl detection with push alerts, recording, two-way talk and camera controls.

- Plan and increments: [docs/PLAN.md](docs/PLAN.md)
- Architecture decisions: [docs/adr/](docs/adr/)
- Developer workflow: [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)

## Status

| # | Increment | State |
|---|-----------|-------|
| 0 | Scaffold: Go service with `/api/health`, web client skeleton, Makefile, docs | done |
| 1 | Camera + audio proof with go2rtc (native capture helper, ADR-0004) | Mac + phone verified, OBS coexistence re-check pending |
| 2 | Tailscale remote access | |
| 3 | Control service supervises go2rtc, pause/resume | |
| 4 | Web client live view | |
| 5 | Auth + HTTPS | |
| 6 | PWA + run at boot | |
| 7 | Howl detection | |
| 8 | Push notifications | |
| 9 | Recording and clips | |
| 10 | Two-way audio | |
| 11 | Camera PTZ controls | |
| 12 | SaaS readiness | |

## Quick start

Requirements: Go 1.25+, Node 24+, Xcode Command Line Tools (for the Swift capture helper),
Homebrew. `deploy/install.sh` installs ffmpeg, go2rtc (pinned release) and builds the helper.

```sh
make build            # compile to bin/watchmydog
make test             # unit tests
make run              # start the API on 127.0.0.1:8080
curl localhost:8080/api/health
```

Web client:

```sh
make web-install      # once
make web-dev          # Vite dev server on :5173, proxies /api to :8080
make web              # production build into web/dist
```

Camera stream proof (increment 1):

```sh
deploy/install.sh     # ffmpeg, go2rtc, wmd-capture
make go2rtc-dev       # go2rtc on :1984 with deploy/go2rtc.yaml; open http://localhost:1984
```

## Layout

```
cmd/watchmydog/   process entrypoint, wiring only
internal/         Go packages (domain, ports, adapters, httpapi, config)
capture/          wmd-capture, native macOS webcam capture helper (Swift)
web/              TypeScript web client (Vite)
deploy/           go2rtc config, launchd plist, install script
docs/             plan, ADRs, developer guide
```
