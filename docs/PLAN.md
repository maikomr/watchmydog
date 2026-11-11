# watchmydog — Remote dog monitor (Mac mini → Android)

## Context

You want to check on your dog from your Android phone while away: live video **and audio** from the
REDRAGON USB webcam plugged into the Mac mini (the howling is the main concern because of neighbour
complaints). Later increments add howl alerts, recording, two-way talk, and optional pan/tilt/zoom.
You may later turn this into a SaaS, so the code must keep clean interfaces between domain logic and
the concrete tools (go2rtc, Tailscale, local disk, Web Push) so each can be swapped for a cloud
counterpart.

The project directory is empty. Environment found on the Mac mini:

| Item | State |
|---|---|
| macOS | 26.6.2 (Apple Silicon) |
| Camera | REDRAGON Live Camera (UVC), also Camo and OBS virtual cameras |
| Mic | REDRAGON Live Camera Audio (default input, 44.1 kHz) |
| Go / Node / Python | 1.25 / 24 / 3.14 installed |
| ffmpeg, Tailscale, Android SDK | not installed |
| Sleep | already prevented by caffeinate |
| LAN IP | 192.168.1.4 |

## Decisions (from your answers)

- **Remote access:** Tailscale (no router changes), with HTTPS from `tailscale serve`.
- **Client:** mobile web app (PWA) first; native Android later.
- **Media engine:** compose existing tools — **ffmpeg + go2rtc** (WebRTC, <1 s latency, H.264 + Opus).
- **Server glue language:** Go. Web client: TypeScript (Vite, no heavy framework).
- **Auth v1:** single shared secret behind Tailscale, behind an `Authenticator` interface.
- **Operations:** Mac stays on; you also use OBS/Camo with the same webcam → camera sharing must be verified and a pause/resume control added.
- **Future features in scope:** howl alert push, recording/clips, two-way audio, PTZ controls (nice-to-have, e.g. Insta360 Link 2).

## Architecture

Hexagonal: the `watchmydog` Go service owns the domain and HTTP API; everything that touches the
outside world is an adapter behind a port interface. The Mac mini is an **edge node**; for SaaS the
same binary would run on the edge and register to a cloud control plane, with media relayed through
a TURN/SFU — nothing in the domain or API should assume a single machine.

```
 Android phone (Chrome PWA)
   │  HTTPS via Tailscale (tailscale serve → :443 → watchmydog :8080)
   ▼
 watchmydog (Go)  ── serves web/, /api/*, /media/* (reverse proxy to go2rtc), /ws/*
   ├─ ports/StreamEngine   ──► adapters/go2rtc      (supervises go2rtc child process, WHEP/WebRTC)
   ├─ ports/SoundDetector  ──► adapters/sound/threshold (ffmpeg RTSP → PCM → RMS dBFS + hysteresis)
   ├─ ports/Notifier       ──► adapters/notify/webpush (VAPID)          [later: FCM]
   ├─ ports/Recorder       ──► adapters/record/ffmpeg (segments on disk) [later: S3]
   ├─ ports/EventStore     ──► adapters/store/sqlite                     [later: Postgres]
   ├─ ports/Authenticator  ──► adapters/auth/sharedsecret                [later: OIDC/multi-tenant]
   └─ ports/CameraControl  ──► adapters/ptz/null, adapters/ptz/uvc
                                      │
                               go2rtc ──► ffmpeg -f avfoundation (REDRAGON video + audio)
                                      └─► exec backchannel → ffplay (Mac speakers, two-way audio)
```

Domain model from day one (single-tenant implementation): `Site` → `Camera` → `Stream`, `Event`
(sound/motion), `Clip`, `PushSubscription`. IDs everywhere, no "the camera" singletons.

## Repository layout

Single private GitHub repo (`maikomr/watchmydog`, monorepo) because the Go service embeds and serves
the web client, every increment touches both halves, and there is one developer. `web/` keeps its own
package.json and build so it can be split out later. A native Android app and any SaaS control plane
get their own repos when they start. Commits authored as
`Maiko Morales <3672014+maikomr@users.noreply.github.com>`.

```
watchmydog/
  cmd/watchmydog/main.go          # wiring only
  internal/domain/                # entities + value objects
  internal/ports/                 # interfaces listed above
  internal/adapters/{go2rtc,auth/sharedsecret,notify/webpush,record/ffmpeg,sound/threshold,store/sqlite,ptz/null}
  internal/httpapi/               # chi/stdlib router, middleware, handlers
  internal/config/                # YAML + env (edge config vs cloud config separated)
  web/                            # Vite + TS PWA (manifest, service worker, WebRTC player)
  deploy/go2rtc.yaml              # media engine config (template)
  deploy/launchd/com.watchmydog.agent.plist
  deploy/install.sh               # brew installs, launchctl load, tailscale serve
  docs/adr/                       # one ADR per decision above
  Makefile                        # build, test, run, lint, web-build
```

Go deps (small): `go-chi/chi` (router), `SherClockHolmes/webpush-go`, `modernc.org/sqlite` (no cgo),
`gopkg.in/yaml.v3`. Web deps: Vite, TypeScript only.

## Increments

Each increment ends with a check you perform yourself. Nothing later starts until the check passes.

### 0. Scaffold
- `git init`, Go module, Vite TS app, Makefile (`make build test run web`), README, `.gitignore`, ADR-001 (architecture), ADR-002 (go2rtc), ADR-003 (Tailscale).
- **Verify:** `make build && make test` succeeds; `make run` prints "listening on :8080" and `curl localhost:8080/api/health` returns `{"status":"ok"}`.

### 1. Camera + audio proof on the Mac

> Outcome note (2026-11-11): ffmpeg's avfoundation input cannot configure this camera on macOS 26,
> so this increment grew a small native Swift helper, `capture/wmd-capture`, that feeds raw frames
> to ffmpeg for encoding. go2rtc is not in Homebrew; `deploy/install.sh` pins a GitHub release.
> See ADR-0004.
- `brew install ffmpeg go2rtc`. List devices: `ffmpeg -f avfoundation -list_devices true -i ""`.
- Write `deploy/go2rtc.yaml` with stream `dog: ffmpeg:device?video=<REDRAGON idx>&audio=<REDRAGON idx>&resolution=1280x720&framerate=30#video=h264#audio=opus` (framerate is mandatory on macOS).
- **Verify:** open `http://localhost:1984` on the Mac → WebRTC tab shows live video and you hear the mic. Then on the phone over home Wi‑Fi, `http://192.168.1.4:1984`, same result. Also open OBS with the REDRAGON camera at the same time and confirm both keep working (camera sharing check). Note the macOS camera/microphone permission prompts that appear and for which process.

### 2. Tailscale
- Install Tailscale on the Mac (brew cask) and on the phone; enable MagicDNS; note the hostname (e.g. `macmini.<tailnet>.ts.net`).
- **Verify:** phone on mobile data, Wi‑Fi off, `http://macmini:1984` plays the stream with audio.

### 3. Control service skeleton (Go)
- `StreamEngine` port + `go2rtc` adapter: starts/supervises go2rtc as a child process with our generated config, exposes `Streams()` and health. Reverse proxy `/media/*` → go2rtc (`:1984`) so the client only ever talks to port 8080.
- `/api/health`, `/api/cameras` (from config), `/api/cameras/{id}/pause|resume` (stops/starts the ffmpeg source so OBS/Camo get the camera back cleanly).
- **Verify:** `make run` starts go2rtc automatically; `curl /api/cameras` lists `dog`; `/media/stream.html?src=dog` plays; `pause` frees the camera (ffmpeg process gone), `resume` brings it back.

### 4. Web client v1 (live view)
- Mobile-first page: WebRTC player using WHEP against `/media/api/webrtc?src=dog`, tap-to-unmute (autoplay policy), connection state, reconnect with backoff (must recover after a pause/resume restart of go2rtc and after the phone sleeps; go2rtc's bundled page does not), fullscreen, quality toggle (720p/360p = two go2rtc streams).
- **Verify:** on the phone via Tailscale, wave at the camera and see it on screen in under 1 s; audio audible; lock/unlock phone → stream resumes.

### 5. Auth + HTTPS
- `Authenticator` port; `sharedsecret` adapter: `/api/login` (secret → signed HttpOnly session cookie), middleware on `/api/*`, `/media/*`, `/ws/*`. Secret from env/config, never in git. Rate-limit login.
- `tailscale serve --bg https / http://127.0.0.1:8080` → valid cert, `https://macmini.<tailnet>.ts.net`.
- **Verify:** without cookie → 401 on API and media; login page works; phone shows the padlock with no warning. Repeat the latency check over HTTPS.

### 6. PWA + run at boot

> Outcome note (2026-11-11): launchd-started processes need their own camera/microphone
> permission, tied to the binary's signature, which changes with every Go build. The service now
> runs under a small stable launcher app (`launcher/`, WatchMyDog.app). FileVault is on, so
> unattended recovery after reboot is not possible without the owner's decision. See ADR-0006 and
> docs/OPERATIONS.md.
- `manifest.json`, service worker (app shell only, never cache media), icons. Install to home screen.
- `deploy/launchd/com.watchmydog.agent.plist` (LaunchAgent in your login session, `KeepAlive`, logs to `~/Library/Logs/watchmydog/`). `deploy/install.sh` installs it. Mac mini set to auto-login.
- **Camera-permission check (risk item):** launch from launchd and confirm macOS grants camera + mic to ffmpeg/go2rtc. If TCC refuses, fallback is wrapping the launcher in a minimal signed `.app` bundle (ADR-004 records the outcome).
- **Keep the Mac awake (finding from increment 5, 2026-11-11):** the Mac idle-slept with the default Energy settings, and a sleeping Mac is offline for Tailscale ("Wake for network access" does not help). The owner has set system sleep to never. In addition, the service holds its own "prevent idle system sleep" power assertion while it runs (same mechanism as `caffeinate -i`), so it does not depend on a setting a macOS update could reset; it is released on shutdown. Enable "Start up automatically after a power failure" (`pmset autorestart 1`) and document auto-login. Note: during development Claude Code's own `caffeinate` kept the Mac awake and masked the problem, so test with no session active.
- **Verify:** reboot the Mac, touch nothing, open the home-screen icon on the phone → live stream within ~1 min. Leave the Mac idle for over an hour with no Claude Code session running, then check from outside: still reachable, and `pmset -g log` shows no idle sleep.

### 7. Howl / noise detection
- `SoundDetector` port; `threshold` adapter: ffmpeg pulls `rtsp://127.0.0.1:8554/dog` audio only → s16le PCM pipe → Go computes RMS dBFS per 100 ms window; "event" when level > threshold for ≥ N s, with hysteresis and cooldown. All tunables in config.
- `EventStore` port + SQLite adapter; `/api/events` (list, filter by time), `/api/events/stream` (SSE for live level meter and events).
- Client: live level meter + event list.
- **Verify:** clap/play a howl recording near the camera → event appears in the client within 2 s; quiet room produces none for 30 min; adjust threshold via config without code change.

### 8. Push notifications
- `Notifier` port; `webpush` adapter (VAPID keys generated by `watchmydog keys`), `/api/push/subscribe`, subscriptions persisted. Noise event → push with deep link to live view. Per-device mute toggle and quiet-hours in config.
- **Verify:** phone locked, Wi‑Fi off; trigger noise → notification arrives; tapping it opens the live view already playing.

### 9. Recording and clips
- `Recorder` port; `ffmpeg` adapter: continuous ring buffer from RTSP (`-c copy -f segment`, 60 s segments, retention N hours) plus event clips assembled from ring segments (pre-roll 15 s, post-roll until quiet + 15 s). `Clip` rows in SQLite; `/api/clips`, `/api/clips/{id}/video.mp4` (range requests).
- Client: clips tab, play inline, delete.
- **Verify:** trigger noise → clip appears listing start time and duration, playback shows the ~15 s before the noise; disk usage stays below the configured cap after 24 h.

### 10. Two-way audio (talk to the dog)
- Primary: go2rtc `exec:ffplay -nodisp -f s16le -ar 48000 -ac 1 -i -#backchannel=1` stream; client WebRTC `sendrecv` with push-to-talk button (mic requires HTTPS, done in step 5).
- Fallback if go2rtc issue #2084 (backchannel shows send-only) bites: `/ws/talk` WebSocket, client `MediaRecorder` Opus/WebM 100 ms chunks → Go → `ffplay` stdin.
- **Verify:** hold talk button on the phone, speak → heard from Mac mini speakers under ~1 s; release → silence; live audio from the dog keeps playing.

### 11. Camera controls (nice-to-have, only when PTZ hardware exists)
- `CameraControl` port; `null` adapter now; `uvc` adapter later (UVC PTZ via libuvc/cgo or `uvc-util`). `/api/cameras/{id}/ptz` with pan/tilt/zoom deltas and presets. Client: on-screen d-pad shown only when the adapter reports capabilities.
- **Verify:** with the Insta360 Link 2 connected, buttons move the camera; with REDRAGON the controls are hidden.

### 12. SaaS readiness (no cloud built yet)
- `Dockerfile` for the Go service (linux/arm64+amd64), OpenAPI spec for `/api`, `edge` vs `cloud` config split, `ports` doc listing each adapter's cloud replacement (FCM, S3, Postgres, OIDC, TURN/SFU), ADR-005 (edge/cloud topology).
- **Verify:** `docker build` passes; `go test ./...` runs adapter contract tests against in-memory fakes; the API spec validates.

## Risks and mitigations

- **macOS TCC camera/mic permission for a launchd-started ffmpeg** — tested in increment 6 with a documented fallback (signed `.app` wrapper). Early signal already in increment 1 (which process gets prompted).
- **Camera contention with OBS/Camo** — macOS generally allows concurrent camera access but the first opener fixes the format; verified in increment 1, pause/resume in increment 3 as the escape hatch.
- **go2rtc backchannel bug (#2084)** — WebSocket fallback in increment 10.
- **Audio codec:** WebRTC needs Opus/PCMA; config transcodes mic to Opus (`#audio=opus`). Verify audio actually plays in increment 1 before building on it.
- **Mobile Chrome autoplay** — tap-to-unmute handled in increment 4.
- **False-positive alerts** — thresholds configurable; ML classifier (YAMNet-style) is a future `SoundDetector` adapter, not v1.

## Out of scope for now

Native Android app, multi-user accounts, cloud deployment, ML sound classification, motion detection.
