# Ports and adapters: today and in the cloud

The application core (`internal/domain`, `internal/app/*`) only talks to the outside world through
the interfaces in `internal/ports`. Each interface has one adapter today, chosen for a single Mac
mini at home. This table lists what would replace it in a hosted product (ADR-0011), and what
proves a replacement behaves the same.

| Port | What it does | Today (edge) | Cloud replacement | Same behaviour checked by |
|---|---|---|---|---|
| `StreamEngine` | Turns cameras into live streams; release/resume | `adapters/go2rtc`: supervised go2rtc, WHEP over Tailscale; wrapped by `app/cameras` to remember released cameras | **Stays at the edge.** Viewers outside the home reach it through a **TURN** relay, or the edge pushes to a **media server / SFU** (LiveKit, mediasoup, Janus) that fans out to viewers | `httpapi` tests with a fake engine; engine tests against a fake go2rtc API |
| `PauseStore` | Remembers released cameras | SQLite | Stays at the edge (it is about the local device) | `storetest.RunPauses` (SQLite, memory) |
| `SoundDetector` | Level and noise events from the microphone | `adapters/sound/threshold`: ffmpeg + RMS threshold | Stays at the edge (audio must not leave home just to be measured). A classifier (bark/howl vs. door) would be a second adapter | `threshold` tests with recorded levels |
| `EventStore` | History of noise events | SQLite (`adapters/store/sqlite`) | **Postgres**, with a `site_id` column; the edge keeps SQLite and uploads events | `storetest.Run` (SQLite, memory) |
| `SubscriptionStore` | Devices that want notifications | SQLite | **Postgres** (subscriptions belong to user accounts, not to one Mac) | `storetest.RunSubscriptions` |
| `Notifier` | Sends a notification to one device | `adapters/notify/webpush`: Web Push with VAPID, no account | Web Push still works from a server. Native apps add **FCM** (Android) and **APNs** (iOS) adapters behind the same interface | `webpush` tests decrypt the message as a browser would |
| `Recorder` | Ring of the last minutes; cuts clips | `adapters/recorder/ffmpeg`: files on the Mac's disk | The ring stays at the edge. Clips are uploaded to **object storage (S3 or compatible)** and played from signed URLs | `recorder/ffmpeg` tests with real ffmpeg; `app/clips` tests with a fake |
| `ClipStore` | List of clips | SQLite | **Postgres** | `storetest.RunClips` |
| `Speaker` | Plays the owner's voice at the camera | `adapters/speaker/ffplay` on the Mac | Stays at the edge. Voice from the cloud reaches it over the edge's connection to the cloud | `ffplay` tests with a silent stand-in |
| `Authenticator` | Login and sessions | `adapters/auth/sharedsecret`: one shared secret, signed session cookie | **OIDC** (an identity provider: Google, Auth0, Cognito, Keycloak) with user accounts and per-site permissions | `sharedsecret` tests; `httpapi` auth tests with a fake |

Not ports today, but would need one in the cloud:

- **Edge link:** how an edge registers with the cloud, uploads events and clips, and receives
  commands (release, talk). Not designed yet; ADR-0011 lists the options.
- **Tenancy:** a `site` (one home, one or more cameras) owned by an account.

## Running the contract tests

`make test` (part of `make check`) runs every adapter's contract test against the in-memory fakes
and SQLite. A new adapter (Postgres, for example) joins by calling the same `storetest.Run*`
function from its own test.
