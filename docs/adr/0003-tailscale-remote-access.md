# ADR-0003: Tailscale for remote access and HTTPS

Date: 2026-11-11 · Status: accepted

## Context

The phone must reach the Mac mini from anywhere. Options: router port forwarding + dynamic DNS,
a cloud relay (TURN/Cloudflare tunnel), or a mesh VPN.

## Decision

Use **Tailscale** on the Mac mini and the phone. `tailscale serve` terminates HTTPS with a valid
certificate for `<host>.<tailnet>.ts.net` and forwards to the Go service on `127.0.0.1:8080`.
WebRTC media flows over the tailnet (direct or DERP-relayed) without extra TURN infrastructure.

## Consequences

- Nothing is exposed to the public internet; no router changes; works behind CGNAT.
- Valid HTTPS is required anyway for the PWA, Web Push and microphone access on the phone, and
  Tailscale gives it for free.
- Application-level auth (shared secret, increment 5) is still added so a compromised tailnet
  device cannot watch the stream.
- A future SaaS replaces this adapter with a cloud relay (TURN/SFU) — the app code does not change.
