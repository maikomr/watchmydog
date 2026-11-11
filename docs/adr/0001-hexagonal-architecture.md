# ADR-0001: Hexagonal architecture with an edge/cloud split in mind

Date: 2026-11-11 · Status: accepted

## Context

watchmydog starts as a single-user service on a Mac mini, but the owner may turn it into a SaaS.
The concrete tools chosen today (go2rtc, local disk, Web Push, shared-secret login, Tailscale) are
right for one home and wrong for a multi-tenant cloud product.

## Decision

Structure the Go service as ports and adapters:

- `internal/domain` holds entities (`Site`, `Camera`, `Stream`, `Event`, `Clip`, `PushSubscription`)
  with IDs from day one; nothing assumes a single camera or a single machine.
- `internal/ports` defines interfaces: `StreamEngine`, `SoundDetector`, `Notifier`, `Recorder`,
  `EventStore`, `Authenticator`, `CameraControl`.
- `internal/adapters` holds one package per implementation; each future cloud replacement
  (FCM, S3, Postgres, OIDC, TURN/SFU) is a new adapter, not a rewrite.
- The Mac mini is an **edge node**. The HTTP API is designed so a cloud control plane could sit in
  front of many edge nodes later.

## Consequences

- Slightly more files than a flat script, but each piece is testable with fakes.
- Wiring happens only in `cmd/watchmydog/main.go`.
- Contract tests per port keep adapters interchangeable.
