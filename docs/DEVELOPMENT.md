# Developer workflow

## Principles

1. **Small, verifiable increments.** Each increment in [PLAN.md](PLAN.md) ends with a manual check
   the owner performs on real hardware. The next one does not start until the check passes.
2. **Tests ship with the code.** Every Go package has unit tests; adapters that talk to external
   processes get contract tests against fakes. `make check` must pass before a commit.
3. **Decisions are written down.** Anything that is not obvious from the code gets an ADR in
   `docs/adr/` (why go2rtc, why Tailscale, what happened with macOS camera permissions, ...).
4. **No secrets in git.** Shared secret, VAPID keys and Tailscale details live in `*.local.yaml`
   or environment variables; `.gitignore` covers them.

## Daily loop

```sh
make check        # fmt + vet + tests
make run          # API on :8080
make web-dev      # client on :5173 with /api proxied
```

`make check` runs Go fmt/vet/tests and the web type-check and vitest suite.

Commit per increment (or per meaningful step inside one) with a message of the form
`inc<N>: <what changed>`. Push to the private GitHub repo `maikomr/watchmydog`.

## Code organisation (hexagonal)

- `internal/domain` – entities and value objects, no imports from adapters.
- `internal/ports` – interfaces the domain needs from the outside world
  (StreamEngine, SoundDetector, Notifier, Recorder, EventStore, Authenticator, CameraControl).
- `internal/adapters/<port>/<impl>` – one package per concrete implementation.
- `internal/app/<service>` – application services that connect ports (`monitor`: detector →
  events → store and live subscribers; `alerts`: event → rules → notifier for every subscribed
  device; `clips`: events → which part of the recording becomes a clip, and how long clips are
  kept; `cameras`: remembers released cameras). No I/O of their own; tested with fakes.
- `internal/httpapi` – HTTP handlers; translate requests into port and service calls, no business rules.
- `cmd/watchmydog` – wiring and process lifecycle only.

Rule of thumb: if a package imports `os/exec`, `net`, `database/sql` or a third-party SDK, it is an
adapter and must sit behind a port.

## Verification log

Record the outcome of each increment's manual check here, with the date, so regressions are easy to
spot later.

| Inc | Date | Result | Notes |
|-----|------|--------|-------|
| 0 | 2026-11-11 | pass | `make check` green, `/api/health` returns ok |
| 1 | 2026-11-11 | pass | ffmpeg avfoundation cannot drive the camera on macOS 26 (ADR-0004); native helper + go2rtc produce 1280x720 H.264 + Opus (verified with `ffprobe` on `/api/stream.mp4?src=dog`). Camera sharing (automated proxy for OBS): a second AVFoundation client could open the camera while go2rtc streamed and go2rtc kept delivering; the first opener's format wins, so a second client asking for another size gets the first one's. Manual: Mac browser pass, phone over Wi-Fi pass. OBS: first try failed (helper stopped on format change, go2rtc restart loop, OBS black); fixed by scaling in the helper; stream-first order pass; OBS-first order failed (config lock refused, AVError -11817, restart loop), fixed by adopting the camera's format; re-check pass |
| 2 | 2026-11-11 | pass | Mac `maikos-mac-mini.tail54b4a7.ts.net`, phone `maikos-s24-ultra`; stream with audio over mobile data via `http://maikos-mac-mini:1984`. MagicDNS + HTTPS certs enabled |
| 3 | 2026-11-11 | pass | Service starts/supervises go2rtc (localhost-only API), `/api/cameras` lists `dog`, `/media/api/stream.mp4?src=dog` yields H.264 through the proxy, `pause` kills the capture process and removes the stream from the generated config, `resume` restores it (log shows one restart each). Phone check: stream plays through the proxy, pause stops it; after resume go2rtc's bundled player page stays on a spinner because it never reconnects its WebSocket, a reload plays again. Reconnect-with-backoff is a requirement for the increment 4 client |
| 4 | 2026-11-11 | pass | 12 vitest unit tests (backoff, WHEP, player reconnect paths), type-check clean, `dog_low` derived stream verified with ffprobe. Phone: controls and status label work, stream recovers by itself after the phone was locked for 1 minute |
| 5 | 2026-11-11 | pass | Over `https://maikos-mac-mini.tail54b4a7.ts.net`: Let's Encrypt cert verifies; health and web client public; session, cameras, pause, media streams and WebRTC signaling return 401 without a session; wrong secret 401; correct secret sets `wmd_session` (HttpOnly, Secure); 6th failed login returns 429 with Retry-After 900; limiter keys on the real tailnet IP (X-Forwarded-For from tailscale serve); logout clears the session; port 8080 refused from the tailnet; H.264 + Opus through HTTPS, auth and media proxy. Phone outside on mobile data: login, logout, wrong-secret message and staying logged in after closing Chrome all work. An apparent outage was the old `:8080` bookmark (closed by design) plus the Mac idle-sleeping before the owner set system sleep to never; see increment 6 |
| 6 | 2026-11-11 | automated part pass | Under launchd via WatchMyDog.app: after the owner clicked Allow, H.264 720p (brightness 179/255, real room) and Opus (mean -19 dB) through HTTPS. Service rebuilt with a new version and reinstalled: launcher untouched, 0 new permission prompts, stream fine. SIGKILL of the service: launcher stopped the leftover go2rtc, healthy again in 1 s, 0 orphans. 3 stream attempts while permission was pending: 0 orphans. `make doctor`: all required checks pass (FileVault on is a warning). Pending: phone install, reboot test, 1 h idle test without Claude Code |
| 6 | 2026-11-11 | reboot test fail, fixed | Phone app installed. After reboot the service came up by itself (healthy), but Tailscale was Stopped because its app is not set to launch at login; opening the app restored HTTPS. Owner to enable "Launch Tailscale at login" and repeat the reboot test |
| 6 | 2026-11-12 | pass | Owner confirmed after enabling "Launch Tailscale at login": reboot test and idle test pass. Decision: FileVault stays on (disk encryption over unattended recovery); after an unplanned reboot someone has to log in at the Mac |
| 7 | 2026-11-12 | automated part pass | `make check` green (Go incl. store contract tests on SQLite and memory, 37 web tests). On the Mac under launchd: detector listening 1.5 s after start with the camera off (no capture process; before the source reorder go2rtc started the camera for ~1 s per connect); quiet room -50 dBFS; `/api/events` and `/api/events/stream` 401 without session, levels arrive 10/s with timestamps within ~10 ms of the clock; video + audio still play on `dog` and `dog_low`. With the threshold temporarily at -45 an event was stored, streamed live, and closed and saved on shutdown; the service stops in under 1 s with a stream open. Pause: one warning, no detection; listening again 1.5 s after resume. Test data removed, threshold back to -30. Not verified: a real trigger at -30 (the Mac's speaker only reached -42 dBFS at the mic and the room was not quiet, so the detection delay could not be measured), and the meter bar moving on a real screen (a headless screenshot showed the layout, threshold mark and event list). Pending: owner's phone check |
| 7 | 2026-11-12 | pass | Owner confirmed the phone check |
| 8 | 2026-11-12 | automated part pass | `make check` green (69 web tests; Go tests include decrypting a sent message as a browser would and verifying its VAPID signature). On the Mac under launchd: `data/vapid.json` created with mode 0600; database migrated to schema 2; `/api/push/*` return 401 without a session; subscribe stores the device, a second subscribe keeps one row; a test send to a made-up FCM endpoint reached Google, was answered "gone" and the device was forgotten (API 410); real noise events in the room were logged with the reason for not notifying (no device switched on); a headless screenshot shows the Notifications section with "Turn on". Not verified: delivery to a real device, the permission prompt, the notification's look and what a tap does (headless Chrome has no push). Pending: owner's phone check |
| 8 | 2026-11-13 | pass | Owner confirmed on the phone: notifications work as expected. Reported separately: live audio has frequent very short cuts (under investigation) |
| 8 | 2026-11-16 | withdrawn | Opus error correction for the live audio (`c3e9ad9`, `1fa7dec`) made it worse on the owner's devices (first 5–10 % gaps, then rapid bursts) although the second version measured clean on the Mac. Reverted to go2rtc's default Opus settings; see the 2026-11-16 amendment of ADR-0002 |
| fix | 2026-11-16 | automated part pass | Released camera survives restarts (`6507dae`). `make check` green. A second copy of the service on other ports with its own data directory: release → restart → still released; kill -9 → start → still released (go2rtc config without the camera, log `cameras stay released from before the restart`); resume → restart → live. Not yet installed on the running service; owner check pending |
