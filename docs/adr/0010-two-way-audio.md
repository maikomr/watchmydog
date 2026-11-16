# ADR-0010: Talking to the dog by uploading the phone's recording in chunks to ffplay

Date: 2026-11-16 · Status: accepted (the delay still has to be measured on the phone)

## Context

The owner wants to say something to the dog from the phone and have it come out of the Mac mini's
loudspeaker, while still seeing and hearing the live view.

The plan named two ways:

1. **go2rtc's backchannel:** the browser sends its microphone over the same WebRTC connection that
   brings the video, and go2rtc pipes it into a player process.
2. **Fallback:** the app records the microphone and sends the recording to our own service, which
   pipes it into a player.

## Decision

The second way, with plain HTTP requests instead of a WebSocket.

### Why not go2rtc's backchannel

- go2rtc hands the player the audio in the codec the browser sent. Browsers send Opus or G.711.
  For Opus go2rtc writes raw network packets a player cannot read (go2rtc issue #2084, open,
  no fix); what works there is G.711 at 8 kHz, which is telephone quality.
- The two-way mode is documented for go2rtc's own WebSocket signalling, not for the WHEP request
  our player uses. The player would have to change, and every viewer would be asked for the
  microphone when connecting.
- Our service would not know when someone talks. It has to: the microphone at the camera hears
  the loudspeaker, so the owner's voice would count as noise and send them a notification.

### How it works

- **Phone:** while the "Hold to talk" button is down, the browser's `MediaRecorder` records the
  microphone as WebM/Opus (32 kbit/s, with the phone's echo cancellation and noise suppression)
  and hands out a piece ("chunk") every 200 ms. The app sends the chunks one after the other.
- **API** (behind login):
  `POST /api/cameras/{id}/talk` opens the loudspeaker and returns a session id;
  `PUT /api/talk/{session}/chunks/{n}` adds chunk number n;
  `DELETE /api/talk/{session}` ends it.
- **Service:** `internal/app/talk` puts the chunks in order (one that arrives early waits, one
  that arrives twice is ignored) and writes them to a `Speaker` port.
- **Mac:** the `ffplay` adapter starts one `ffplay` per session, with read-ahead switched off,
  and pipes the stream into it. ffplay plays on the Mac's current audio output. Playing sound
  needs no macOS permission.

Numbered HTTP requests instead of a WebSocket: Go's standard library has no WebSocket server, so
it would be a new dependency, and requests pass through `tailscale serve` and the existing login
and logging unchanged. The price is one small request per chunk (five a second while talking).

### Rules

- One session per camera. A new one ends the old one.
- A session ends by itself when no chunk arrived for 3 s (the phone lost its connection) or after
  2 minutes (a stuck button must not keep the loudspeaker open).
- While someone talks, and for `sound.min_duration + sound.quiet_time + 5 s` after the last
  chunk, **no notification is sent** for that camera. The noise event is still listed and still
  gets a clip.
- The app silences the live sound while the button is down and for 1.5 s after: otherwise the
  phone plays the owner's own voice back about a second late.
- The first press only makes the browser ask for the microphone. If the finger is up when the
  answer comes, nothing is sent and the app says to hold the button.
- If ffplay is not installed, talking is switched off (logged at start) and the rest of the
  service runs.

## Consequences

- Sound quality is that of Opus at 48 kHz, well above telephone quality.
- Delay is the sum of: one chunk (200 ms), the upload, and ffplay's start and output buffer. It
  is **not measured yet**, neither on the Mac nor on the phone; the plan's target is under about
  a second. If it is too slow, the first things to try are a shorter chunk and keeping ffplay
  running between presses.
- Every press starts a new ffplay; its start-up is part of the delay of the first words.
- Works in Chrome and Firefox. Safari records in another format; there the button is shown
  disabled with a note.
- The owner's voice shows up in the noise events and in clips. Only notifications are held back.
- The sound comes out of whatever output the Mac currently uses, at the Mac's volume
  (`talk.volume` only scales ffplay's part). A Mac mini's built-in speaker is small.
- It works while the camera is released: the loudspeaker does not depend on the camera.
- Not built: talking from two devices at once, choosing the output device, a visible level meter
  for the phone's microphone.
- No new Go or web dependencies.
