# watchmydog

Watch your dog from your phone while you are away. A Go service on a Mac mini streams the USB webcam's
video **and audio** over WebRTC to an Android phone through a Tailscale VPN. It detects noise (howling)
and notifies the phone. Later increments add recording, two-way talk and camera controls.

- Plan and increments: [docs/PLAN.md](docs/PLAN.md)
- Architecture decisions: [docs/adr/](docs/adr/)
- Developer workflow: [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)
- Running it on the Mac mini: [docs/OPERATIONS.md](docs/OPERATIONS.md) (`make doctor`)

## Status

| # | Increment | State |
|---|-----------|-------|
| 0 | Scaffold: Go service with `/api/health`, web client skeleton, Makefile, docs | done |
| 1 | Camera + audio proof with go2rtc (native capture helper, ADR-0004) | done |
| 2 | Tailscale remote access | done |
| 3 | Control service supervises go2rtc, pause/resume | done |
| 4 | Web client live view (WHEP player, reconnect, quality toggle) | done |
| 5 | Auth + HTTPS (shared secret, tailscale serve) | done |
| 6 | PWA + run at boot + keep the Mac awake | done |
| 7 | Noise detection: live level meter, event history (ADR-0007) | done |
| 8 | Push notifications: Web Push, per-device switch, cooldown, quiet hours (ADR-0008) | done |
| 9 | Recording and clips: ring of the last minutes, a clip per noise with 15 s before and after (ADR-0009) | done |
| 10 | Two-way audio: hold a button to talk through the Mac's loudspeaker (ADR-0010) | done |
| 11 | Camera PTZ controls | |
| 12 | SaaS readiness: edge/cloud roles, Docker image, OpenAPI spec, ports guide (ADR-0011) | done |

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

To run it permanently (starts at login, restarts on crash, keeps the Mac awake):

```sh
make install-agent    # then click Allow on the two WatchMyDog prompts on the Mac's screen, once
make doctor           # checks the whole setup and explains fixes
```

Open `https://<mac-hostname>.<tailnet>.ts.net` on the phone and log in with the secret. In Chrome's
menu, "Add to home screen" / "Install app" installs it like an app. The
service listens on localhost only; `tailscale serve` provides HTTPS (docs/TAILSCALE.md). Every API
and media route needs a session cookie (ADR-0005).

The service starts and supervises go2rtc itself and serves the web client at `/`. Player endpoints
are proxied under `/media/`. `POST /api/cameras/dog/pause` releases the camera for other apps;
`POST /api/cameras/dog/resume` takes it back; the web client has buttons for both.

Noise detection (ADR-0007): the service listens to the microphone all the time (the camera stays
off while nobody watches), stores a "noise event" in `data/watchmydog.db` whenever it is loud for
long enough, and streams the live level to the app. Tunables are in the `sound:` section of the
config. API: `GET /api/events` (history) and `GET /api/events/stream` (server-sent events).

Recording (ADR-0009): the service keeps the last minutes of video and audio on disk and saves a
clip for every noise, starting 15 s before it. Because of that the camera runs all the time.
Clips live in `data/recordings/clips`, are limited by `recording.max_disk_mb` and
`recording.keep_for`, and are listed under "Clips" in the app. API: `GET /api/clips`,
`GET /api/clips/{id}/video.mp4`, `DELETE /api/clips/{id}`.

Talking (ADR-0010): "Hold to talk" in the app records the phone's microphone and the Mac plays it
on its loudspeaker with `ffplay`. No notifications are sent while talking. API:
`POST /api/cameras/{id}/talk`, `PUT /api/talk/{session}/chunks/{n}`, `DELETE /api/talk/{session}`.

Cloud readiness (ADR-0011): `role: cloud` runs the service without media; `make docker` builds a
25 MB image of it for linux/amd64 and arm64. The API is described in `docs/openapi.yaml`, and
`docs/PORTS.md` lists what would replace each adapter in a hosted product.

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
internal/         Go packages (domain, ports, adapters, app, httpapi, config)
capture/          wmd-capture, native macOS webcam capture helper (Swift)
launcher/         WatchMyDog.app, stable parent that holds camera/mic permission (Swift)
web/              TypeScript web client (Vite)
deploy/           go2rtc config, launchd plist, install script
docs/             plan, ADRs, developer guide
```
