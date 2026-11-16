# ADR-0002: go2rtc + ffmpeg as the media engine

Date: 2026-11-11 · Status: accepted

## Context

We need low-latency (<1 s) video **and audio** from a USB webcam on macOS to a phone browser, and
later two-way audio. Options considered: writing our own WebRTC server (Pion), MediaMTX, go2rtc,
HLS via ffmpeg alone.

## Decision

Use **go2rtc** (single Go binary) with **ffmpeg** capturing the webcam through AVFoundation.
go2rtc provides WebRTC (WHEP) output with H.264 + Opus, an MSE fallback, an RTSP output we can tap
for audio analysis and recording, and an `exec` backchannel for two-way audio. Our Go service
supervises go2rtc as a child process, generates its config, and reverse-proxies it under `/media/`.

Rejected: own Pion server (weeks of codec/ICE work before the first stream); HLS (5–10 s latency,
useless for two-way audio); MediaMTX (good, but weaker macOS device capture story and no backchannel).

## Consequences

- Fast first stream; we write glue, UI, auth and features instead of media plumbing.
- Dependency on go2rtc behaviour: pinned version in `deploy/`, and a known issue (#2084, backchannel
  advertised send-only) has a WebSocket fallback planned in increment 10.
- WebRTC requires Opus/PCMA audio, so the mic is transcoded with `#audio=opus`.

## Amendment 2026-11-11: supervision and pause/resume (increment 3)

The Go service owns go2rtc: it renders `data/go2rtc.yaml` from its own config, starts go2rtc as a
child process, restarts it with backoff if it dies, forwards its log lines, and proxies its player
endpoints under `/media/`. go2rtc's API therefore binds to localhost only.

Pause/resume of a camera is implemented by re-rendering the config without/with that camera and
restarting go2rtc. go2rtc's runtime API can add and delete stream entries, but deleting an entry
does not stop its capture processes while viewers are attached, so the camera would not be released
deterministically. A restart takes a fraction of a second, kills every capture process, and the
web client reconnects on its own. This is acceptable for a single-site edge node; a multi-camera
cloud deployment would revisit it.

## Amendment 2026-11-16: a released camera stays released across restarts

The paused state lived only in the engine's memory. Any restart of the service (crash, reboot,
`make install-agent`) started every camera again and took the device back from the program the
owner had released it for.

- New port `PauseStore` (`SetPaused`, `PausedCameras`), implemented by the SQLite store
  (migration 3, table `paused_cameras`) and the in-memory fake, with a shared contract test.
- `StreamEngine.Start` takes the list of cameras that start paused. go2rtc's first config leaves
  them out, so the device is not opened even for a moment.
- `internal/app/cameras` wraps the engine: it reads the list at start and writes every pause and
  resume. The HTTP layer is unchanged.
- Order of the two steps: a pause is stored **before** the engine acts, a resume is removed from
  the store **after** the engine acted. If the service dies in between, the camera ends up
  released, never taken back.
- If the list cannot be read at start, the service does not start (launchd retries). Starting
  with all cameras could interrupt the owner's call.
- The database is now opened before the media engine and closed after it.

## Amendment 2026-11-16: Opus error correction tried and withdrawn

Short audio cuts were reported on mobile data. Opus in-band error correction (FEC) was tried by
overriding go2rtc's `opus` ffmpeg template (`-application:a audio -fec 1 -packet_loss 10`).

- First version (`c3e9ad9`) left out go2rtc's `-min_comp 0` and lost 5–14 % of the audio at the
  source.
- Second version (`1fa7dec`) restored it and measured clean on the Mac (50 packets/s without
  holes, 0 % concealed in a headless browser on loopback), but the owner heard the audio as rapid
  bursts on a real device.

Both were reverted. The encoder settings are go2rtc's defaults again
(`-c:a libopus -application:a lowdelay -min_comp 0`), which play cleanly on the local network and
with occasional short cuts on mobile data.

What to take from it:

- A clean measurement on loopback does not show how the stream plays on the phone. An audio
  change counts as verified only after listening on the phone.
- The cause of the bursts with the second version is not known. A new attempt should start from
  the phone's `audio gaps` readout and `chrome://webrtc-internals`, not from the encoder.
