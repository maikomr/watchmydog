# ADR-0009: Recording as a ring of short files, clips joined from it without re-encoding

Date: 2026-11-16 · Status: accepted

## Context

Increment 7 lists noise events and increment 8 sends a notification, but by the time the owner
opens the app the noise is often over. They want to see and hear what happened, including the
seconds **before** the noise started.

Constraints:

- A clip that begins before the noise needs footage of the past. Something has to record all the
  time.
- The Mac mini's disk is shared with everything else on it; recordings must not fill it.
- The stream already exists in go2rtc as H.264 video and Opus audio. Encoding it a second time
  would cost CPU for nothing.

## Decision

### The ring

A **ring** (ring buffer) keeps only the most recent minutes: new footage comes in, the oldest is
thrown away.

- One ffmpeg per camera reads the stream from go2rtc over RTSP on localhost and writes it
  **without re-encoding** (`-c copy`) as a chain of files of about 10 seconds ("segments"):
  `data/recordings/ring/<camera>/20261116T193041Z.mkv`. The name is the UTC time the segment
  begins; the file's last-modified time is its end.
- A segment can only begin at a key frame (a complete picture; the stream has one every 2 s), so
  segments are 10 s give or take 2 s.
- Segments older than the ring length are deleted. The ring length is not configured; it follows
  from the rules below: `max_clip + pre_roll + 2 min` (7 min 15 s with the defaults, about 140 MB).
- ffmpeg is restarted with a pause of 1 to 5 s when it ends, like the sound detector. While the
  camera is released there is no stream and so no recording; the loss is logged once.

The plan had 60-second segments and a ring of several hours. Shorter segments make clips more
exact (see below), and nothing reads hours of ring: only clips do.

### Clips

`internal/app/clips` decides what becomes a clip. It is hooked into the monitor's "event started"
and "event ended":

- A noise opens a clip that begins `pre_roll` (15 s) before it.
- The clip ends `post_roll` (15 s) after the noise ended. A new noise before that moment goes into
  the **same** clip. A dog barking on and off gives one clip, not ten overlapping ones.
- A clip is at most `max_clip` (5 min) long. A noise that goes on gets a second clip that begins
  where the first ends.

To make the file, the recorder joins the segments that overlap the wanted span into one MP4, again
without re-encoding (ffmpeg's `concat` reader), with the index at the front of the file
(`+faststart`) so a browser can start playing before it has everything.

Consequence of joining whole segments: **a clip starts and ends on segment boundaries**. It can
begin up to 10 s earlier and end up to 10 s later than asked, never later or earlier. Cutting
exactly would need re-encoding (or a cut at a key frame, which is at most 2 s more exact for much
more complexity).

A clip is cut when the segment that holds its last second is finished, so it appears in the app
about 20 to 30 s after the noise ended.

### Storage and limits

- Video: `data/recordings/clips/<id>.mp4`. List (camera, start, length, size): table `clips` in
  the SQLite database (migration 4). At start the two are compared: a file without an entry or an
  entry without a file is removed (both can only come from a crash).
- `max_disk_mb` (10 000): when all clips together are bigger, the oldest are deleted.
- `keep_for` (30 days): older clips are deleted; `0` keeps them until the disk limit removes them.
- The clean-up runs at start, after every new clip and once an hour.
- At about 2.5 Mbit/s a minute of clip is roughly 20 MB, so the default holds about 8 hours of
  clips.

### Ports and API

- `Recorder` port (`Record`, `Cut`, `Open`, `Delete`, `Stored`), adapter
  `internal/adapters/recorder/ffmpeg`. `ClipStore` port, implemented by the SQLite store and the
  in-memory fake with a shared contract test.
- `GET /api/clips?camera=&limit=` (newest first, plus used and maximum disk space),
  `GET /api/clips/{id}/video.mp4` (range requests, so the player can jump; `?download=1` saves it
  as a file), `DELETE /api/clips/{id}`. All behind login.
- The app has a "Clips" section under the noise events: play inline, save, delete. The list is
  reloaded every 15 s while the app is visible.

## Consequences

- **The camera now runs around the clock**, not only while someone watches, and its light stays
  on. Before, only the microphone was always on. This is the price of the seconds before the
  noise. `recording.enabled: false` returns to the old behaviour.
- CPU, measured on the Mac mini with `ps`: capture about 11 % of one core, H.264 encoding
  (hardware) about 8 %, the recorder itself about 1.5 %.
- The ring is written continuously: about 20 MB a minute, 27 GB a day, to the internal SSD.
- No recording while the camera is released. A noise during that time appears in the event list
  (if the microphone still delivers) without a clip.
- A clip that is still being collected when the service stops is lost. The ring files survive a
  restart, so a clip cut after the restart can still reach back before it.
- A clip is as long as its segments: up to about 20 s longer than `pre_roll + noise + post_roll`,
  and up to 20 s longer than `max_clip`.
- Clips are H.264 + Opus in MP4. Chrome and Firefox play that; Safari only in recent versions.
  The owner's phone uses Chrome.
- Recording follows sound detection: with `sound.enabled: false` nothing would ever be cut, so
  recording is switched off too (logged at start).
- Clips are not linked to single events in the database; a clip and the events inside it are
  matched by time. Enough for one list each; a link can be added when the app needs it.
- Not built: thumbnails, a timeline of the ring, recording on demand, clips for the low-quality
  stream.
