# ADR-0007: Sound detection by level threshold, events in SQLite, live updates over SSE

Date: 2026-11-12 · Status: accepted

## Context

The main reason for this project is the dog howling while nobody is home. Increment 7 has to notice
noise, keep a history, and show the level live in the app. Push alerts (increment 8) and clips
(increment 9) build on the same events.

Measured on the Mac mini's REDRAGON microphone, through the same path the detector uses:

- A quiet room reads about **-50 dBFS** (between -45 and -58 in single 100 ms windows).
- When go2rtc opens the microphone, the first ~300 ms contain a **full-scale pop** (-6, -12, -20 dBFS).
- A test tone from the Mac mini's own speaker at system volume 44 reached only **-42 dBFS**, so the
  built-in speaker is not a useful stand-in for a dog.

**dBFS** ("decibels relative to full scale") is the unit used throughout: 0 is the loudest value
the microphone can report, every -6 dB is half the amplitude, and digital silence is reported as
-100.

## Decision

### Where the audio comes from

The detector is one more viewer of the stream: an `ffmpeg` process reads
`rtsp://127.0.0.1:8554/<camera>?audio=opus` from go2rtc, decodes it to mono 16 kHz 16-bit samples
and writes them to a pipe that the Go service reads.

- It reuses the audio capture that live viewing already has: one process owns the microphone, and
  the detector needs **no macOS permission** (it only talks to localhost).
- It is always connected, so the microphone is always on. The **camera stays off** while nobody
  watches: go2rtc starts a stream's sources in order until the viewer has all the tracks it asked
  for, so the generated go2rtc config now lists the audio source before the video source. With the
  old order go2rtc started the camera for about a second on every detector connect (seen in the
  log on 2026-11-12).
- When the camera is released for OBS/Camo ("pause"), the stream does not exist and the detector
  retries every few seconds, logging the loss once. **There is no detection while paused.**
- If ffmpeg gets no data for 5 s it exits and is restarted. The first second after every connect
  is discarded because of the pop.

Rejected: a second `ffmpeg -f avfoundation` on the microphone (needs its own permission prompt and
duplicates capture); decoding Opus in Go (a codec dependency for no gain).

### What counts as a noise event

The level is the RMS of each 100 ms window, in dBFS. A small state machine
(`internal/adapters/sound/threshold/tracker.go`) turns levels into events:

```
idle ──loud──► pending ──loud time ≥ min_duration──► active (event stored, clients told)
  ▲               │                                     │
  └── quiet for quiet_time ◄────────────────────────────┘ (event ends)
```

| Setting | Default | Meaning |
|---|---|---|
| `threshold_dbfs` | -30 | A window at or above this is loud |
| `hysteresis_db` | 6 | It is quiet again only below threshold minus this (-36). Stops flicker around the threshold |
| `min_duration` | 1s | Loud time needed to open an event. Windows need not be consecutive, so barking with gaps adds up; a door slam does not |
| `quiet_time` | 5s | Quiet time that ends an event. Bursts closer together than this are one event |

An event's start is the first loud window, its end the last window that was not quiet, and its
peak the loudest window. Timestamps are derived from the sample count, so durations are exact.

The plan mentioned a cooldown between events. `quiet_time` already merges bursts into one event,
and a detector-level cooldown would hide real howling from the history, so there is none. Limiting
how often the phone is **notified** belongs to increment 8.

This is a loudness detector, not a howl recogniser: a vacuum cleaner or loud TV also triggers it.
A classifier would be another adapter behind the same `SoundDetector` port.

### Where events are stored

`EventStore` port with a SQLite adapter (`data/watchmydog.db`), using `modernc.org/sqlite`, a
pure-Go build of SQLite: no C toolchain, still one binary. It is pinned to v1.59.0, the newest
release that builds with Go 1.25. Schema changes are numbered migrations tracked in SQLite's
`user_version`. An event is saved when it starts (no end yet) and saved again when it ends, so it
is visible while the noise is still going on. If the service dies mid-event, the next start closes
the leftover event with a length of zero, which the app shows as "length unknown".

An in-memory store implements the same port for tests. Both run the same contract test
(`internal/adapters/store/storetest`), which a Postgres adapter would have to pass too.

### How the app gets live data

- `GET /api/events?camera=&since=&until=&limit=` lists stored events, newest first.
- `GET /api/events/stream?camera=` is a **server-sent events** (SSE) stream: one long HTTP response
  on which the server writes small messages: `hello` (thresholds and ongoing events), `level`
  (about 10 per second) and `sound` (an event started or ended).

SSE instead of a WebSocket because the data flows one way, the browser's `EventSource` reconnects
by itself and sends the session cookie, and it passes through `tailscale serve` as plain HTTP. The
app closes the stream while it is in the background. A client that falls behind loses messages
instead of slowing the detector; the app reloads the list on every reconnect.

### Code structure

A new application-service layer, `internal/app/monitor`, connects the ports: it runs the detector,
gives detections an ID, stores them and fans updates out to subscribers. The HTTP layer and, later,
the push notifier are subscribers. It has no I/O of its own.

## Consequences

- The microphone is captured and one extra ffmpeg decodes audio around the clock (measured with
  `ps`: about 5 % of one core for capture and Opus encoding, about 1 % for the detector's decoder).
- The default threshold is a guess for this room; it has to be tuned with the live meter
  (docs/OPERATIONS.md). Changing it needs `make restart-agent`.
- Events are kept forever. At a few hundred bytes each that is not a concern; retention can be
  added with recording in increment 9.
- The mic level depends on the macOS input volume of the device (currently 100). Changing it shifts
  every reading.
