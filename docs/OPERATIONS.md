# Operations runbook (Mac mini)

Everything needed to keep the camera reachable, and what to do when it is not.
Design background: [ADR-0006](adr/0006-launchd-agent-and-permissions.md).

## Health check first

```sh
make doctor
```

It checks power settings, the keep-awake lock, Tailscale and `tailscale serve`, the launchd agent,
the health endpoint, HTTPS on the tailnet and the secret file, and prints how to fix each problem.

## Day to day

| Task | Command |
|---|---|
| Install or update the service (after pulling changes) | `make install-agent` |
| Is it running? | `make agent-status` |
| Follow the log | `make logs` (file: `~/Library/Logs/watchmydog/watchmydog.log`) |
| Restart | `make restart-agent` |
| Stop and remove from login | `make uninstall-agent` |
| Release the camera for OBS/Camo | "Release camera" button in the app. It stays released until "Resume camera" is tapped, also across restarts of the service or the Mac |

The agent starts at login and restarts the service if it exits for any reason.

## One-time host setup

1. **Energy:** System Settings > Energy > turn on "Prevent automatic sleeping when the display is
   off". The service also holds its own keep-awake lock, so this is a second line of defence.
2. **Restart after a power failure:** `sudo pmset -a autorestart 1`
   (or System Settings > Energy > "Start up automatically after a power failure").
3. **Camera and microphone permission:** after the first `make install-agent`, macOS shows two
   prompts on the Mac's screen for **WatchMyDog**. Click Allow on both. They are listed afterwards
   under System Settings > Privacy & Security > Camera / Microphone. Service updates do not ask
   again.
4. **Tailscale:** menu bar icon > Settings > turn on "Launch Tailscale at login". Without it the
   service starts after a reboot but the Mac stays off the tailnet (seen on 2026-11-11: state
   "Stopped" until the app was opened). See
   [TAILSCALE.md](TAILSCALE.md) for `make serve`.

## Noise detection: tuning

The app shows a level meter under the video. The number is the loudness in dBFS: 0 is the loudest
the microphone can report, a quiet room here is about -50. The orange mark is the alert level.
Background: [ADR-0007](adr/0007-sound-detection.md).

To tune, edit the `sound:` section of `deploy/watchmydog.local.yaml`, then `make restart-agent`:

| Setting | Default | Change it when |
|---|---|---|
| `threshold_dbfs` | -30 | Howling is missed: lower it (e.g. -35). Everyday sounds trigger it: raise it (e.g. -25). Watch the meter while the noise happens and put the threshold a few dB below what it reaches |
| `min_duration` | 1s | Short sounds (a door, one bark) should count: lower it. `100ms` reacts to a single clap |
| `quiet_time` | 5s | One howling session shows up as many events: raise it |
| `hysteresis_db` | 6 | Rarely needed |
| `enabled` | true | `false` switches detection off; the history stays readable |

Facts worth knowing:

- Detection needs the stream: while the camera is released for OBS/Camo there is **no detection**.
- The microphone is always on. With recording on (the default) the camera is too; with
  `recording.enabled: false` it runs only while someone watches.
- Events live in `data/watchmydog.db` (SQLite). Look at them with
  `sqlite3 data/watchmydog.db "select datetime(started_at/1000,'unixepoch','localtime'), (ended_at-started_at)/1000.0, peak_dbfs from events order by started_at desc limit 20"`.
  Deleting the file while the service is stopped clears the history.
- It reacts to loudness, not to what the sound is: a vacuum cleaner counts too.

## Recording and clips

Background: [ADR-0009](adr/0009-recording-and-clips.md).

Every noise event gets a clip: video and sound from about 15 s before the noise until about 15 s
after it. Clips are under "Clips" in the app (play, save to the phone, delete). A clip appears
about half a minute after the noise ended.

Settings, in the `recording:` section of `deploy/watchmydog.local.yaml`, then `make restart-agent`:

| Setting | Default | Meaning |
|---|---|---|
| `enabled` | true | `false`: no recording, and the camera only runs while someone watches. Old clips stay playable |
| `pre_roll` | 15s | How long before the noise a clip begins |
| `post_roll` | 15s | How long after the noise it goes on. A new noise within this time goes into the same clip |
| `max_clip` | 5m | Longest single clip. A longer noise gets several clips |
| `max_disk_mb` | 10000 | Disk space for all clips. The oldest are deleted to stay below it. About 20 MB per minute of clip |
| `keep_for` | 720h | Clips older than this are deleted. `0s`: only the disk limit deletes |

Facts worth knowing:

- **The camera runs around the clock** and its light stays on: the seconds before a noise can only
  be in the clip if they were already recorded.
- Files: `data/recordings/clips/<id>.mp4` (the clips) and `data/recordings/ring/` (the last
  minutes, about 150 MB, rewritten all the time). Do not put files of your own there: at start,
  clip files the service does not know are deleted.
- A clip can be up to 10 s longer at each end than the settings say.
- No recording while the camera is released for OBS/Camo.
- `make logs` shows `clip saved` and `clip deleted` (with the reason).
- Disk use: `du -sh data/recordings/*`.

## Notifications

Background: [ADR-0008](adr/0008-push-notifications.md).

Each device decides for itself: in the app, under "Notifications", tap **Turn on** and allow
notifications when Chrome asks. **Send test** sends one to that device only. **Turn off** stops
them for that device.

A notification is sent when a noise event starts. Rules are in the `push:` section of
`deploy/watchmydog.local.yaml`; apply with `make restart-agent`:

| Setting | Default | Meaning |
|---|---|---|
| `cooldown` | 5m | At most one notification per camera in this time. `0s` notifies about every event |
| `quiet_hours` | none | `start`/`end` as `"23:00"`/`"07:00"`, on this Mac's clock. No notifications in between; events are still listed in the app |
| `enabled` | true | `false` switches notifications off for all devices |
| `contact` | placeholder | Email or https URL passed to the push service with each notification |

Facts worth knowing:

- Notifications travel over the internet through Google's push service (for Chrome), encrypted so
  Google cannot read them. They arrive without Tailscale; opening the app from one needs Tailscale.
- `data/vapid.json` is the key pair that signs them. Keep it with the rest of `data/`. If it is
  lost, every device has to tap Turn on again.
- A phone that is offline for more than an hour does not get that notification later.
- Subscribed devices: `sqlite3 data/watchmydog.db "select substr(endpoint,1,40), datetime(created_at/1000,'unixepoch','localtime') from push_subscriptions"`.
- `make logs` shows a line for every event: `notification sent`, or `no notification` with the
  reason (cooldown, quiet hours, no device).

## Reboots, power cuts and FileVault

This Mac has **FileVault on**. macOS then does not allow automatic login, and after any reboot the
Mac waits at the unlock screen. Until someone types the password, neither Tailscale nor the service
runs, so the camera is unreachable.

- **Planned restarts** (e.g. installing a macOS update while away): `sudo fdesetup authrestart`
  unlocks the disk once for the next boot. The Mac then logs in and everything starts by itself.
- **Unplanned reboots** (power cut, crash, automatic update): someone has to log in at the Mac.

**Owner's decision (2026-11-12): FileVault stays on.** Disk encryption matters more than
unattended recovery, so the limitation above is accepted.

For the record, the alternative: turn FileVault off (System Settings >
Privacy & Security > FileVault) and enable System Settings > Users & Groups > "Automatically log in
as". The service works either way.

## When something is wrong

| Symptom | Check |
|---|---|
| App says "Server unreachable" | Phone: Tailscale on? Mac: awake and logged in? `make doctor` |
| Connects but video is black or there is no sound | `make logs`: look for `camera permission`. Then System Settings > Privacy & Security > Camera / Microphone: WatchMyDog must be on |
| "Camera released for other apps" | Someone tapped Release: tap "Resume camera". A restart does not resume it (`make logs` shows `cameras stay released from before the restart`) |
| Login always says wrong secret | `cat data/secret` on the Mac; rotate: delete it, `make secret`, `make restart-agent` (logs out all devices) |
| Too many attempts | Wait the shown minutes, or `make restart-agent` to clear the lockout |
| Sound meter shows "–" | No audio is reaching the detector: camera released, or `make logs` shows `audio unavailable` |
| No noise events although it was loud | Threshold too high for the room: see "Noise detection: tuning" |
| Too many noise events | Threshold too low or `min_duration` too short: same section |
| No notification although the event is in the list | `make logs`: `no notification` gives the reason (cooldown, quiet hours, no device). If it says `notification sent`: on the phone check Do Not Disturb, Settings > Apps > watchmydog (or Chrome) > Notifications, and that battery use is not "Restricted" |
| "Send test" says the subscription has expired | Tap Turn off, then Turn on |
| App says notifications are blocked | Chrome > site settings for the app's address > Notifications > Allow, then reopen the app |
| A noise event has no clip | Wait half a minute. Then `make logs`: `no clip: nothing was recorded` (camera was released, or the service had just started) or `recording unavailable` |
| Clips disappear | `make logs`: `clip deleted` gives the reason. Raise `recording.max_disk_mb` or `recording.keep_for` |
| A clip does not play on a device | Clips are H.264 + Opus in MP4: use Chrome or Firefox. "Save" downloads the file |
| Service crashed | Nothing to do: launchd restarts it (verified: healthy again about 1 s after a kill) |

## Log size

The log grows by roughly tens of kilobytes per hour of viewing. Truncate it if it ever gets large:
`: > ~/Library/Logs/watchmydog/watchmydog.log` (the agent keeps writing to the same file).
