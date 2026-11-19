# ADR-0011: Edge and cloud roles of one service; Docker image for the cloud role

Date: 2026-11-19 · Status: accepted (the cloud role is a starting point, not a product)

## Context

watchmydog runs on one Mac mini at home and is reached over Tailscale. The plan keeps the option
of a hosted product (several homes, user accounts, native apps) without building it now. The
last increment should leave the code ready for that step:

- a container image of the service,
- a description of the API that others can build against,
- a clear line between what must stay at home and what could run in a data centre,
- for each port, what would replace today's adapter.

## Decision

### Two roles, one binary

The config gets `role: edge | cloud` (default `edge`).

- **edge** is what runs on the Mac today: cameras, go2rtc, sound detection, recording, the
  loudspeaker, keep-awake. Nothing changes for it.
- **cloud** has no media. It refuses a `cameras` section, and switches off keep-awake, sound
  detection, recording and talking whatever the file says. It serves login, the API (cameras list
  empty, events and notification settings from its own database) and the web client. The routes
  that need media (release/resume, talk, clips, `/media/`) are not registered there.

One binary instead of two: the HTTP layer, auth, stores and notifications are shared, and the
edge-only parts were already optional in the HTTP layer.

What the cloud role does **not** do yet: receive anything from an edge. It is the skeleton that
the edge link (below) will plug into.

### What stays at the edge, and why

| Stays at home | Reason |
|---|---|
| Capture, encoding, the ring | Needs the camera and macOS permissions; uploading video all the time would cost bandwidth and privacy |
| Sound detection | Audio should not leave the home just to be measured |
| Loudspeaker | It is the Mac's |
| Released-camera state | It is about the local device |

| Could move to the cloud | How |
|---|---|
| Accounts, login | OIDC instead of the shared secret |
| Event history, clip list, push subscriptions | Postgres, with a site id |
| Clip videos | Uploaded by the edge to object storage |
| Notifications | Sent from the cloud (Web Push, FCM, APNs) |
| Live view from outside | TURN relay, or the edge pushes to an SFU |

Per port details: [docs/PORTS.md](../PORTS.md).

### Edge link (not built)

The edge would open an outgoing connection to the cloud (no port forwarding at home): a
WebSocket or gRPC stream for events and commands, and uploads of clips to signed object-storage
URLs. Tailscale stays the way the owner reaches the edge directly. To decide when the cloud is
built.

### Docker image

- `Dockerfile` at the repo root, three stages: the web client (Node), the service (Go,
  `CGO_ENABLED=0`, cross-compiled for the target platform, so `linux/amd64` and `linux/arm64`
  build without emulation), and the runtime on `distroless/static` (no shell, unprivileged user
  65532). The image is about 25 MB.
- It runs the cloud role with `deploy/watchmydog.cloud.yaml`: listens on `0.0.0.0:8080`, keeps
  the database and VAPID keys in the volume `/data`, takes the login secret from
  `WATCHMYDOG_SECRET`.
- TLS is not in the image: it belongs to the load balancer or reverse proxy in front. The session
  cookie is `Secure`, so the app does not work over plain HTTP from another machine.
- `.dockerignore` lets in only what the build copies, so `data/`, secrets and local configs
  cannot end up in the image.
- The edge is not containerised: Docker on macOS runs Linux in a virtual machine without access to
  the Mac's camera, microphone or loudspeaker.

### API description

`docs/openapi.yaml` (OpenAPI 3.1) describes every route, its parameters, bodies, answers and
errors; routes that only the edge has are marked `x-edge-only`. Two Go tests keep it honest: one
fails when a route in `server.go` is missing from the spec or the spec describes one that does
not exist, the other when a `$ref` does not resolve. `make api-lint` checks it against the
OpenAPI rules with Redocly's linter (downloaded by `npx`, pinned version).

## Consequences

- The plan named this ADR-005; that number was taken (shared-secret auth), so it is ADR-0011.
- The cloud role is not useful to anyone yet: without the edge link it has no cameras and no
  events. Its value is that the shared code now runs without a Mac, which the container proves.
- The spec is written by hand. The tests catch missing and extra routes, not wrong field names in
  bodies; those are checked by reading, and by the client's own types in `web/src/api.ts`.
- Adding an edge-only feature now means: register its routes only when its service is set, and
  leave it out of the cloud wiring in `cmd/watchmydog`.
