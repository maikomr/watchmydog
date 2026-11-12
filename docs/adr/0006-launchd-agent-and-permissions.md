# ADR-0006: Run as a launchd agent behind a stable launcher app; keep the Mac awake

Date: 2026-11-11 · Status: accepted

## Context

The service must survive crashes and reboots, keep the Mac reachable, and keep its camera and
microphone permission without anyone at the Mac. Findings on this Mac mini (macOS 26.6):

1. **Sleep.** With default Energy settings the Mac idle-slept and was offline for Tailscale.
   "Wake for network access" does not wake it for tailnet traffic. Development hid this: Claude
   Code runs `caffeinate` while working.
2. **Camera/microphone permission (TCC) under launchd.** macOS asks on behalf of the *responsible
   process*, which for a launchd agent is the program launchd starts. Starting the Go binary
   directly made macOS prompt for `~/.local/bin/watchmydog` (verified in the TCC log:
   `AUTHREQ_PROMPTING ... responsible_path=/Users/mmorales/.local/bin/watchmydog`). Until someone
   clicks Allow at the Mac, AVFoundation delivers no frames. Go binaries are ad-hoc signed by the
   linker, so their identity changes with every build and the permission would be asked again
   after every update.
3. **FileVault is on.** macOS disables automatic login with FileVault, so after any reboot nothing
   of ours runs until someone unlocks the disk at the Mac.
4. **Orphaned capture processes.** The capture helper ignored SIGTERM (its signal handlers ran on a
   blocked main queue) and never noticed its parent dying while no frames flowed. One orphan ran
   for 35 minutes.

## Decision

- **Per-user launchd agent** `com.watchmydog.agent` (`~/Library/LaunchAgents`), not a system daemon:
  camera and microphone belong to the logged-in user's session. `RunAtLoad`, `KeepAlive` (restart
  after any exit), `ThrottleInterval` 10 s, `ProcessType` Interactive, logs in
  `~/Library/Logs/watchmydog/`. Managed with `deploy/agent.sh` / `make install-agent`.
- **Stable launcher app.** launchd starts `~/Applications/WatchMyDog.app` (a ~60-line Swift program
  with an Info.plist carrying `NSCameraUsageDescription` and `NSMicrophoneUsageDescription`). It
  runs the service as its child, forwards SIGTERM/SIGINT/SIGHUP and exits with the child's status.
  The service, go2rtc, ffmpeg and wmd-capture inherit its permission. The install script copies the
  app only when its contents changed, so service updates never trigger new prompts.
  When the service exits, the launcher stops every other process left in its process group
  (SIGTERM, then SIGKILL after 1.5 s), because launchd did not: an ffmpeg from a previous agent
  instance survived a restart. It only does this when it leads its own group, as under launchd.
- **Keep awake.** The service holds an IOKit `PreventUserIdleSystemSleep` assertion named
  "watchmydog: serving the camera stream" while it runs (`keep_awake: true`, default). The kernel
  drops it if the process dies. The display still sleeps; manual sleep still works.
- **Capture helper hardening.** Signals on a dedicated queue; a watchdog, started before any
  AVFoundation call (those block while a permission prompt waits), exits when the parent disappears
  and exits with code 3 after 10 s without a written frame, so go2rtc restarts it; a clear log
  line when camera permission is pending or denied. Verified: three stream attempts with the
  permission pending leave no orphaned processes.
- **`make doctor`** checks every host setting this depends on (sleep, keep-awake lock, power-failure
  restart, FileVault/auto-login, Tailscale and serve, agent, health, HTTPS, secret file mode) and
  explains each fix. It never changes settings.

## Consequences

- One click on Allow per permission at the Mac, once. After that, updates are hands-off. If
  `launcher/` ever changes, the prompts return once.
- With FileVault on, a power cut or update reboot leaves the camera offline until someone logs in at
  the Mac. The alternative (FileVault off + automatic login) trades disk encryption for unattended
  recovery; it is the owner's decision and documented in docs/OPERATIONS.md.
- A Linux edge node would use a systemd unit and `systemd-inhibit`; the `power` package already has
  a no-op for other platforms and nothing else depends on launchd.

## Amendment 2026-11-12: FileVault stays on

The owner decided to keep FileVault on. Unattended recovery after an unplanned reboot is therefore
not available by design; `sudo fdesetup authrestart` covers planned restarts. Increment 6's reboot
and idle checks passed once Tailscale was set to launch at login.
