# ADR-0008: Notifications with Web Push (VAPID), per-device subscriptions, cooldown and quiet hours

Date: 2026-11-12 · Status: accepted

## Context

Increment 7 detects noise and lists it in the app, but only someone looking at the app sees it.
The owner wants the phone to alert them, also when it is locked and the app is closed.

Constraints:

- The client is a web app installed from Chrome on Android (PWA). There is no native app.
- The Mac is only reachable through Tailscale. It can make outgoing internet requests.
- No cloud accounts so far, and none wanted for this.

## Decision

### Web Push

**Web Push** is the browser standard for this (RFC 8030). The parts:

1. The app asks the browser for a **subscription**. The browser returns an **endpoint** (a URL at
   the browser vendor's push service; for Chrome that is Google's FCM) and two keys of the device.
2. The app gives the subscription to our server, which stores it.
3. To notify, the server POSTs an encrypted message to the endpoint. The push service wakes the
   phone and hands the message to the app's **service worker**, a script the browser runs in the
   background even when the app is closed. The service worker shows the notification.

Two keys protect this:

- The message is encrypted to the device's keys (RFC 8291). Google's service relays it but cannot
  read it.
- The request is signed with the server's **VAPID** key pair (RFC 8292). A subscription only
  accepts messages signed with the key it was created for, so nobody else can send to our devices.
  VAPID needs no account or API key at Google.

Rejected: polling from the app (does not work with the app closed), a native Android app with FCM
(a second codebase and a Google project), a third-party service such as ntfy or Pushover (another
app on the phone, another party sees the alerts).

### Server

- `Notifier` port, `internal/adapters/notify/webpush` adapter built on
  `github.com/SherClockHolmes/webpush-go` v1.4.0. A test plays the browser's part: it decrypts the
  request body following RFC 8291 and verifies the VAPID signature.
- The VAPID key pair is created on first start in `data/vapid.json` (mode 0600, git-ignored). The
  plan had a `watchmydog keys` command; creating the file automatically removes a setup step and
  cannot be forgotten.
- `SubscriptionStore` port, implemented by the existing SQLite store (migration 2, table
  `push_subscriptions`) and the in-memory fake, with a shared contract test.
- `internal/app/alerts` is the application service. The monitor calls it when an event **starts**;
  it queues the event and returns at once, so the detector never waits for the network. A worker
  applies the rules, sends to all devices in parallel and deletes a device when the push service
  says its subscription is gone (HTTP 404 or 410).
- API, all behind login: `GET /api/push/config` (public key and rules),
  `POST /api/push/subscribe`, `POST /api/push/unsubscribe`, `POST /api/push/test`.

### Rules

- **Cooldown** (`push.cooldown`, default 5 min): at most one notification per camera in that time.
  It starts when a notification was delivered to at least one device.
  This is the cooldown that ADR-0007 left out of the detector: the history still shows every
  event, only the alerts are limited.
- **Quiet hours** (`push.quiet_hours`, default none): a daily span on the Mac's clock without
  notifications. Events are still detected and listed. A suppressed event does not start the
  cooldown.
- Messages are sent with urgency `high` (deliver although the phone is dozing) and a lifetime of
  one hour: a phone that was offline longer gets nothing, instead of a stale alert.
- One notification per camera is visible at a time: a newer one replaces the older one (same tag)
  and alerts again.

### Client

- Notifications are switched on **per device**, with a button in the app. Browsers only show the
  permission prompt after a tap. The switch is the "per-device mute" of the plan: off removes the
  subscription in the browser and on the server.
- On every start the app tells the server about its subscription again. That repairs a server
  that lost its database and costs one small request.
- If the server's VAPID key changed, the old subscription is useless; the app drops it and shows
  the switch as off.
- "Send test" asks the server for a test notification to this device only. It ignores cooldown and
  quiet hours.
- Tapping a notification focuses the open app or opens `/?camera=<id>`. The start page is the live
  view, which starts playing (muted, as always until the first tap).
- The service worker shows a generic notification when it cannot read a message, because Chrome
  requires a visible notification for every push. It only ever opens paths inside the app.

## Consequences

- **A third party is involved for the first time.** Each notification is a request from the Mac to
  the push service of the phone's browser vendor (Google for Chrome). Google sees: that this
  server sent something to that device, when, the message size, the VAPID public key and the
  contact string. It does not see the content. Video and audio still only travel over Tailscale.
- VAPID requires a contact (email or URL) in every request. The default is the placeholder
  `watchmydog@example.invalid`, so nothing personal is sent unless `push.contact` is set. Google
  accepts a placeholder today; Apple's service is stricter, which matters only if an iPhone is
  ever used.
- Notifications need the internet on both sides, but not Tailscale. Opening the app from the
  notification does need Tailscale on the phone.
- Delivery is best effort. Android can delay or drop pushes for apps under battery restriction or
  when the notification category is silenced; the runbook lists what to check.
- If `data/vapid.json` is lost, every device has to switch notifications on again. If only the
  database is lost, devices re-register by themselves the next time the app is opened.
- The endpoint URL is what lets a sender reach a device, so logs show only the push service's host
  and the last six characters.
- A logged-in user can make the server POST to any `https` URL by subscribing with it. Accepted:
  only the owner can log in, and the request body is not under their control.
- Not built: changing a browser-replaced subscription in the background (`pushsubscriptionchange`);
  the app re-registers at its next start instead. Rules are global, not per device.
- New dependencies: `webpush-go`, and through it `golang-jwt/jwt` v5.3.1 and `golang.org/x/crypto`
  v0.52.0 (the newest that builds with Go 1.25).
