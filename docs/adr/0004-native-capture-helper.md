# ADR-0004: Native Swift capture helper instead of ffmpeg's avfoundation input

Date: 2026-11-11 · Status: accepted

## Context

The plan assumed go2rtc's `ffmpeg:device` source would capture the REDRAGON webcam. On this
Mac mini (macOS 26.6.2, Apple Silicon) every ffmpeg 9.0.2 **and** 7.1.5 invocation that names a
video size or frame rate logs `Configuration of video device failed, falling back to default` and
then delivers zero frames (the process hangs). Only the unconfigured default works, and that is the
1920x1080 `yuvs` mode at ~5 fps. Audio-only capture through ffmpeg works fine.

Findings from probing with a small Swift program:

- Camera and microphone are already authorized for processes started from this session.
- The camera reports its frame rate as 30.00003 fps, not 30, and rejects an exact 1/30 duration.
  ffmpeg has a 0.01 tolerance so this alone is not the cause.
- The device class on macOS 26 is `AVCaptureDevice_Tundra`, a new capture stack. ffmpeg sets
  `activeFormat` and frame durations through Key-Value Coding (`setValue:forKey:`); that path throws
  here, which is the `@catch` message above.
- The 720p/1080p "420v" modes are MJPEG on the wire, decoded by the system
  (`decompressed_from_format_type = dmb1`).
- With the AVFoundation API used directly, 1280x720 nv12 at 30 fps streams fine **only if the
  device configuration lock is held across `AVCaptureSession.startRunning()`**. Setting the format
  before or after adding the input, using `beginConfiguration`, or the `.hd1280x720` preset all end
  with the session switching the device back to 1080p.

## Decision

Add `capture/wmd-capture`, a ~250-line Swift command-line tool built with `swiftc` (Xcode Command
Line Tools, no Xcode project):

- `wmd-capture list` prints permissions, devices and formats as JSON (used later by the API).
- `wmd-capture run --device NAME --width W --height H --fps F` configures the camera natively and
  writes raw NV12 frames to stdout, validating every frame's geometry.

go2rtc runs it through an `exec:` source piped into ffmpeg, which hardware-encodes H.264 with
VideoToolbox and pushes RTSP back into go2rtc. Audio stays on go2rtc's `ffmpeg:device` source. One
go2rtc stream with the two producers gives WebRTC consumers both tracks.

## Consequences

- One more binary to build and ship (`make capture`, `make install-capture`); it is macOS-only,
  which is fine because the edge node is a Mac. A Linux edge node would use ffmpeg's `v4l2` input
  instead, behind the same `StreamEngine` port.
- Raw 720p30 NV12 through a pipe is ~41 MB/s; negligible on this machine.
- The helper is the process that touches the camera, so macOS camera permission (TCC) attaches to
  it. Increment 6 verifies that under launchd.
- macOS allows several processes on one camera, but the first opener fixes the active format.
  If OBS/Camo open the REDRAGON camera first at another size, the helper's geometry guard stops it
  and go2rtc restarts the producer; increment 3's pause/resume and a later "adapt to the active
  format" option in the helper are the mitigations.
- Future option: encode H.264 inside the helper with VideoToolbox and feed go2rtc's exec pipe
  directly, removing ffmpeg from the video path.
