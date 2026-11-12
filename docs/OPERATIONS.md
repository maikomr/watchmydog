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
| Release the camera for OBS/Camo | "Release camera" button in the app |

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

## Reboots, power cuts and FileVault

This Mac has **FileVault on**. macOS then does not allow automatic login, and after any reboot the
Mac waits at the unlock screen. Until someone types the password, neither Tailscale nor the service
runs, so the camera is unreachable.

- **Planned restarts** (e.g. installing a macOS update while away): `sudo fdesetup authrestart`
  unlocks the disk once for the next boot. The Mac then logs in and everything starts by itself.
- **Unplanned reboots** (power cut, crash, automatic update): someone has to log in at the Mac.

If unattended recovery matters more than disk encryption, turn FileVault off (System Settings >
Privacy & Security > FileVault) and enable System Settings > Users & Groups > "Automatically log in
as". That is a security trade-off for the owner to make; the service works either way.

## When something is wrong

| Symptom | Check |
|---|---|
| App says "Server unreachable" | Phone: Tailscale on? Mac: awake and logged in? `make doctor` |
| Connects but video is black or there is no sound | `make logs`: look for `camera permission`. Then System Settings > Privacy & Security > Camera / Microphone: WatchMyDog must be on |
| "Camera released for other apps" | Someone tapped Release: tap "Resume camera" |
| Login always says wrong secret | `cat data/secret` on the Mac; rotate: delete it, `make secret`, `make restart-agent` (logs out all devices) |
| Too many attempts | Wait the shown minutes, or `make restart-agent` to clear the lockout |
| Service crashed | Nothing to do: launchd restarts it (verified: healthy again about 1 s after a kill) |

## Log size

The log grows by roughly tens of kilobytes per hour of viewing. Truncate it if it ever gets large:
`: > ~/Library/Logs/watchmydog/watchmydog.log` (the agent keeps writing to the same file).
