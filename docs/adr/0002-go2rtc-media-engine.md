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

## Amendment (2026-11-16): Opus error correction

On mobile data the owner heard frequent, very short cuts in the live audio; on the Mac itself it
was clean. The server's audio is continuous (its sample count stayed within about 50 ms of real
time over 80 minutes), so the cuts are packets lost on the network.

go2rtc's built-in Opus settings use `-application lowdelay`. That mode (CELT only) cannot carry
Opus **in-band FEC** (forward error correction: each packet also holds a small copy of the previous
one, so the player can rebuild a lost packet from the next). The generated config now overrides the
`opus` template with `-c:a libopus -application:a audio -fec 1 -packet_loss 10 -min_comp 0`.

**`-min_comp 0` must stay.** The first version of this change left it out (it looked like an
unrelated leftover of go2rtc's default) and broke the audio: the owner saw 5-10 % "audio gaps" with
no packet loss. Opus needs 48 kHz while the microphone delivers 44.1 kHz, so ffmpeg resamples; the
option makes the resampler follow the capture timestamps smoothly. Measured on this Mac by
capturing the same microphone with several settings at once and counting holes in the packet
timestamps:

| Settings | Audio missing at the source |
|---|---|
| go2rtc default (`lowdelay`, `-min_comp 0`) | 0 % |
| `audio` + FEC, without `-min_comp 0` | 5-14 % (a 12 ms hole several times a second) |
| `lowdelay` + FEC, without `-min_comp 0` | 12 % |
| `audio` + FEC + `-min_comp 0` (current) | 0 % |

A browser on the Mac itself (no network) reported 4.7-6.1 % concealed audio before the fix and
0.00 % after.

Measured with a script that reads the mode and the FEC flag from each packet:

| Settings | Encoder mode | Packets carrying FEC (synthesized speech) |
|---|---|---|
| go2rtc default (`lowdelay`) | CELT | 0 % |
| new | hybrid | 81 % (70 % on the live microphone with speech from the Mac's speaker) |

The bitrate is about the same (around 90 kbit/s for speech), the added delay about 4 ms. The
encoder adds FEC to voice-like sound (howls, barks, speech), not to steady background noise, where
a gap is not audible anyway. FEC repairs single lost packets; it does not help when several in a
row are lost or when packets are only late. The app's stats line shows what is left as
`audio gaps X% (lost Y%)`.

