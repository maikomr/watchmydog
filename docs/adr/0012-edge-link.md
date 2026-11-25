# ADR-0012: Edge link — how a home Mac connects to the cloud

Date: 2026-11-25 · Status: proposed (design only; nothing built yet)

## Context

ADR-0011 split the service into an `edge` role (the Mac with the camera) and a `cloud` role (no
media), and left out the piece that joins them. Without it the cloud has no cameras and no
events. This ADR designs that piece. It must work with what the edge does today:

- the live view is WebRTC (WHEP) from go2rtc, reached over Tailscale;
- the sound level arrives 10 times a second over SSE, and noise events are stored in SQLite;
- clips (about 1.2 GB a day, capped at 10 GB; owner's check of 2026-11-24) live on the Mac's disk;
- release/resume and talking are HTTP calls on the edge;
- notifications are Web Push sent by the edge.

Constraints:

- **No inbound ports at home.** The home router stays closed; only the edge dials out.
- **Media stays home unless someone watches** (ADR-0011): no continuous video upload.
- **The Mac goes offline sometimes** (reboots, network). The cloud must say so and must not
  lose events.
- **Tailscale keeps working** as the direct way in, with or without the link.
- One owner, one site today; the design must not block several sites per account later.

## Decision

### 1. Transport: one outgoing WebSocket

The edge opens `wss://<cloud>/edge/v1/link` and keeps it open. Everything travels over it as
framed messages: small JSON messages for control, binary frames for talk audio.

Why a WebSocket rather than the alternatives:

| Option | Against it |
|---|---|
| gRPC bidirectional stream | Needs HTTP/2 end to end; many proxies and load balancers handle it badly; adds protobuf tooling |
| A generic HTTP tunnel (cloudflared style), cloud just proxies the edge's API | Simple at first, but the cloud owns no data: history disappears when the Mac is offline, and notifications, accounts and several sites cannot move to the cloud |
| MQTT | Another broker to run and secure for one connection per home |
| **WebSocket** | Plain HTTPS on port 443, passes every proxy, one small Go library (`github.com/coder/websocket`), easy to test with `httptest` |

Keepalive: a ping every 20 s; the cloud marks the edge offline after 45 s without traffic. The
edge reconnects with exponential backoff (1 s up to 60 s, with jitter).

Messages carry `type`, `id` (for replies) and a body. The protocol version is in the URL
(`/edge/v1/`); an incompatible change gets `/edge/v2/`, and the cloud serves both for a while.

### 2. Pairing and identity: an edge key pair

1. The owner, logged in to the cloud, clicks "Add a home" and gets a **pairing code**
   (8 characters, valid 10 minutes, usable once).
2. On the Mac: `watchmydog pair https://cloud.example <code>`. The edge creates an
   **Ed25519 key pair** (`data/edge.key`, mode 0600, never in git), sends the public key and the
   code, and receives its `site_id`. Both go into `data/link.json`.
3. On each connection the cloud sends a random challenge; the edge signs it with its key. The
   cloud checks the signature against the stored public key.

The cloud stores only public keys, so a leaked cloud database cannot be used to impersonate a
home. "Remove this home" in the cloud deletes the key, and the edge is refused from then on.

### 3. Who owns what

| Data | Source of truth | Over the link |
|---|---|---|
| Cameras, streams, released state | Edge | Edge sends the full list on connect and on every change |
| Noise events | Edge (SQLite), copied to the cloud | Outbox (below) |
| Sound level | Edge | Only while a cloud viewer watches that camera (`levels.subscribe` / `levels.unsubscribe`) |
| Clip list | Edge, copied to the cloud | Outbox, same as events |
| Clip videos | Edge disk | Uploaded only on request (section 5) |
| Accounts, push subscriptions for the cloud app | Cloud | Not sent |

**Events and clips: an outbox with sequence numbers.** The edge numbers every event and clip
change (`seq`, increasing, kept in SQLite) and sends it. The cloud stores it keyed by
`(site_id, event id)`, so a repeat does no harm, and acknowledges the highest `seq` it has
stored. On reconnect the edge resends everything after the last acknowledged `seq`. Nothing is
lost while the Mac is offline from the cloud, and nothing is counted twice.

### 4. Commands from the cloud

The cloud sends a request, the edge answers with the same `id`, and the cloud gives up after
10 s:

- `camera.release`, `camera.resume`;
- `clip.delete`;
- `clip.upload` (section 5);
- `webrtc.offer` (section 6).

The edge does not check users: it trusts commands that arrive over its own authenticated link,
and the cloud checks that the user may act on that site before sending. The HTTP API on the
Tailscale side is unchanged.

**Talking:** the cloud forwards the browser's chunks (the existing WebM/Opus stream of
ADR-0010) as binary frames tagged with a talk id, with `talk.start` and `talk.end` around them.
On the edge they go into the same `Speaker` port as today.

### 5. Clips: upload on request

When a viewer opens a clip in the cloud app and the cloud has no copy, the cloud sends
`clip.upload` with a **signed object-storage URL** (S3 or compatible, PUT, valid 15 minutes).
The edge uploads the MP4 straight to storage, not through the link, then answers. The cloud keeps
the copy for 30 days and plays it from a signed GET URL.

Not uploading every clip saves about 1.2 GB a day of upload and storage, and keeps videos at home
unless the owner asks. The cost: a clip cannot be watched from the cloud while the Mac is offline,
and the first play waits for the upload (a 2-minute clip is a few seconds on a home connection).

### 6. Live view: WebRTC signalling over the link, media peer-to-peer with TURN

The browser sends its WHEP offer to the cloud; the cloud passes it over the link
(`webrtc.offer`); the edge hands it to go2rtc and returns the answer. The media then flows
directly between the browser and the Mac whenever the networks allow it (STUN), and through a
**TURN relay** when they do not (mobile data behind carrier NAT, strict firewalls).

- TURN: `coturn` run next to the cloud, or a managed service. Short-lived TURN credentials (the
  TURN REST scheme, valid 1 hour) are handed out by the cloud to both the browser and the edge.
- go2rtc gets the STUN/TURN servers in its WebRTC config.
- No SFU: one or two viewers per home do not need one. An SFU (LiveKit or similar) becomes
  worth it with many viewers per camera or recording in the cloud; the `StreamEngine` port leaves
  room for it.

### 7. Notifications

Each side notifies its own subscribers: the edge sends Web Push to devices that subscribed
through the Tailscale app (as today), and the cloud sends to devices that subscribed through the
cloud app, triggered by the events arriving through the outbox. The two apps have different
origins, so a device is subscribed to one or the other, not both. When the cloud has native apps
it adds FCM/APNs (PORTS.md).

### 8. Configuration

Edge (`watchmydog.yaml`), off unless set:

```yaml
link:
  url: https://cloud.example   # the cloud to connect to; pairing fills data/link.json
```

Cloud: `edge_link: true`, a TURN section (URL and shared secret from the environment) and an
object-storage section (bucket, endpoint, credentials from the environment).

### 9. Security

- TLS only; the edge checks the cloud's certificate normally (no pinning, so certificates can be
  rotated).
- Message size limit (64 KB for JSON, 16 KB per audio frame) and a rate limit per site.
- The edge refuses unknown message types and logs them; the cloud does the same.
- The pairing code is single-use, short-lived and rate-limited per account.
- The edge key is created on the Mac and never leaves it.

### 10. Ports

New ports, so the core does not know about WebSockets:

- `EdgeLink` (edge side): publishes cameras, events and clips; receives commands.
- `SiteRegistry` (cloud side): sites, public keys, online state, last seen.
- `EdgeGateway` (cloud side): sends a command to a site and waits for the answer; the HTTP layer
  uses it instead of `StreamEngine`/`Speaker` when the role is `cloud`.

Fakes of each, with contract tests, as for the other ports.

## Increments

Each one ends with something the owner can check:

| # | What | Check |
|---|---|---|
| 13 | Pairing, the connection with reconnect and keepalive, camera list and online state | The local cloud container shows the Mac's camera and goes "offline" when the service stops |
| 14 | Outbox for events and clip list | Events made while the cloud was down appear after it is back, none twice |
| 15 | Commands: release/resume, delete clip, talk | Releasing from the cloud app frees the camera; the dog hears a voice sent through the cloud |
| 16 | Live view through the cloud: signalling, STUN, local coturn | Live view from the cloud app on the phone on mobile data |
| 17 | Clip upload on request (MinIO locally, S3-compatible) | Opening a clip in the cloud app uploads and plays it |

The empty-state screen for a cloud with no cameras (ADR-0011) comes with increment 13.

Still out of scope: user accounts and OIDC, several sites in the UI, Postgres, native apps,
hosting. Increments 13–17 run against the local container; the cloud keeps SQLite with a
`site_id` column.

## Consequences

- The cloud owns the history and notifications for its app, so the Mac being offline only
  affects live view, talking and clips that were never uploaded.
- Two copies of events (edge and cloud) are kept consistent by the outbox; the edge stays the
  source of truth.
- TURN adds a service to run and pay for (traffic of relayed live views). Many connections will
  go peer-to-peer and cost nothing.
- One new dependency on the edge and in the cloud: `github.com/coder/websocket`.
- The Tailscale path is untouched; a Mac without `link:` behaves exactly as today.
