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
| 1 | Camera + audio proof with go2rtc (native capture helper, ADR-0004) | done |
| 2 | Tailscale remote access | done |
| 3 | Control service supervises go2rtc, pause/resume | done |
| 4 | Web client live view (WHEP player, reconnect, quality toggle) | done |
| 5 | Auth + HTTPS (shared secret, tailscale serve) | done |
| 6 | PWA + run at boot + keep the Mac awake | next |
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
make secret           # once: create the login secret in data/secret (cat it to see it)
make run              # start the service with deploy/watchmydog.local.yaml (created from the example)
make serve            # once: publish it on the tailnet over HTTPS
curl localhost:8080/api/health
```

Open `https://<mac-hostname>.<tailnet>.ts.net` on the phone and log in with the secret. The
service listens on localhost only; `tailscale serve` provides HTTPS (docs/TAILSCALE.md). Every API
and media route needs a session cookie (ADR-0005).

The service starts and supervises go2rtc itself and serves the web client at `/`. Player endpoints
are proxied under `/media/`. `POST /api/cameras/dog/pause` releases the camera for other apps;
`POST /api/cameras/dog/resume` takes it back; the web client has buttons for both.

Web client (`web/`): a WHEP WebRTC player with tap-to-unmute, reconnect with backoff, a stall
watchdog (reconnects when frames stop, e.g. after a go2rtc restart), fullscreen and a high/low
quality toggle. The low variant is a second go2rtc stream (`dog_low`) derived from the main one,
enabled with `video.low` in the config. Unit tests: `make web-test`.

Web client:

```sh
make web-install      # once
make web-dev          # Vite dev server on :5173, proxies /api and /media to :8080
make web              # production build into web/dist (make run does this)
make web-test         # vitest unit tests
```

First-time tools: `deploy/install.sh` (ffmpeg, go2rtc, wmd-capture). `make go2rtc-dev` still runs
go2rtc standalone with `deploy/go2rtc.yaml` for troubleshooting the capture pipeline.

## Layout

```
cmd/watchmydog/   process entrypoint, wiring only
internal/         Go packages (domain, ports, adapters, httpapi, config)
capture/          wmd-capture, native macOS webcam capture helper (Swift)
web/              TypeScript web client (Vite)
deploy/           go2rtc config, launchd plist, install script
docs/             plan, ADRs, developer guide
```
