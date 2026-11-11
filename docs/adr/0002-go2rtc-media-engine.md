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
