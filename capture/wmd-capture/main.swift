// wmd-capture: native AVFoundation webcam capture for watchmydog.
//
// Why this exists: on macOS 26 ffmpeg's avfoundation input fails to configure
// the REDRAGON camera ("Configuration of video device failed") and then
// delivers no frames, while the AVFoundation API itself works. This tool does
// the device configuration natively and writes raw frames to stdout, where
// ffmpeg encodes them. See docs/adr/0004-native-capture-helper.md.
//
// Usage:
//   wmd-capture list                          JSON of permissions, devices, formats
//   wmd-capture run --device NAME [--width W --height H --fps F --seconds S --adopt-format --stall-timeout S]
//                                             raw NV12 frames to stdout, logs to stderr
//
// Output is always W x H NV12 at the camera's rate. If the camera is in (or is
// switched by another app to) a different format, frames are scaled with
// VideoToolbox; --adopt-format skips configuring the camera altogether.
//
// Build: swiftc -O -o bin/wmd-capture capture/wmd-capture/main.swift

import AVFoundation
import Foundation
import VideoToolbox

// MARK: - Helpers

func log(_ msg: String) {
    FileHandle.standardError.write((msg + "\n").data(using: .utf8)!)
}

func fail(_ msg: String, code: Int32 = 1) -> Never {
    log("wmd-capture: " + msg)
    exit(code)
}

func fourCC(_ code: FourCharCode) -> String {
    let bytes = [UInt8((code >> 24) & 0xff), UInt8((code >> 16) & 0xff), UInt8((code >> 8) & 0xff), UInt8(code & 0xff)]
    return String(bytes: bytes, encoding: .ascii) ?? String(code)
}

func authName(_ s: AVAuthorizationStatus) -> String {
    switch s {
    case .notDetermined: return "notDetermined"
    case .restricted: return "restricted"
    case .denied: return "denied"
    case .authorized: return "authorized"
    @unknown default: return "unknown"
    }
}

/// Writes all bytes, retrying on partial writes. Returns false on EPIPE or
/// another unrecoverable error (reader went away).
func writeAll(fd: Int32, _ ptr: UnsafeRawPointer, _ len: Int) -> Bool {
    var off = 0
    while off < len {
        let n = write(fd, ptr + off, len - off)
        if n < 0 {
            if errno == EINTR { continue }
            return false
        }
        off += n
    }
    return true
}

func videoDevices() -> [AVCaptureDevice] {
    AVCaptureDevice.DiscoverySession(
        deviceTypes: [.external, .builtInWideAngleCamera, .continuityCamera],
        mediaType: .video, position: .unspecified
    ).devices
}

// MARK: - list

struct FormatInfo: Encodable {
    let width: Int32
    let height: Int32
    let pixelFormat: String
    let maxFps: [Double]
}

struct DeviceInfo: Encodable {
    let name: String
    let uniqueID: String
    let inUseByAnotherApplication: Bool
    let formats: [FormatInfo]
}

struct ListOutput: Encodable {
    let cameraAuthorization: String
    let microphoneAuthorization: String
    let devices: [DeviceInfo]
}

func runList() {
    let devices = videoDevices().map { d -> DeviceInfo in
        var seen = Set<String>()
        var formats: [FormatInfo] = []
        for f in d.formats {
            let dims = CMVideoFormatDescriptionGetDimensions(f.formatDescription)
            let cc = fourCC(CMFormatDescriptionGetMediaSubType(f.formatDescription))
            let fps = f.videoSupportedFrameRateRanges.map { ($0.maxFrameRate * 1000).rounded() / 1000 }
            let key = "\(dims.width)x\(dims.height)\(cc)\(fps)"
            if seen.insert(key).inserted {
                formats.append(FormatInfo(width: dims.width, height: dims.height, pixelFormat: cc, maxFps: fps))
            }
        }
        return DeviceInfo(name: d.localizedName, uniqueID: d.uniqueID,
                          inUseByAnotherApplication: d.isInUseByAnotherApplication, formats: formats)
    }
    let out = ListOutput(
        cameraAuthorization: authName(AVCaptureDevice.authorizationStatus(for: .video)),
        microphoneAuthorization: authName(AVCaptureDevice.authorizationStatus(for: .audio)),
        devices: devices)
    let enc = JSONEncoder()
    enc.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try! enc.encode(out)
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write("\n".data(using: .utf8)!)
}

// MARK: - run

struct RunOptions {
    var device = ""
    var width: Int32 = 1280
    var height: Int32 = 720
    var fps: Double = 30
    var seconds: Double = 0 // 0 = until stdout closes or signal
    var adoptFormat = false // never reconfigure the camera; scale whatever it delivers
    var stallTimeout: Double = 10 // exit when no frame arrived for this long (0 = never)
}

func parseRun(_ args: ArraySlice<String>) -> RunOptions {
    var o = RunOptions()
    var it = args.makeIterator()
    while let a = it.next() {
        func value() -> String {
            guard let v = it.next() else { fail("missing value for \(a)") }
            return v
        }
        func number<T: LosslessStringConvertible>(_ v: String, as: T.Type) -> T {
            guard let n = T(v) else { fail("bad value for \(a): \(v)") }
            return n
        }
        switch a {
        case "--device": o.device = value()
        case "--width": o.width = number(value(), as: Int32.self)
        case "--height": o.height = number(value(), as: Int32.self)
        case "--fps": o.fps = number(value(), as: Double.self)
        case "--seconds": o.seconds = number(value(), as: Double.self)
        case "--adopt-format": o.adoptFormat = true
        case "--stall-timeout": o.stallTimeout = number(value(), as: Double.self)
        default: fail("unknown option \(a)")
        }
    }
    if o.device.isEmpty { fail("--device is required (see `wmd-capture list`)") }
    return o
}

let nv12 = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange

final class FrameWriter: NSObject, AVCaptureVideoDataOutputSampleBufferDelegate {
    private(set) var frames = 0
    private let countLock = NSLock()
    /// Frames written so far; safe to read from any thread.
    var frameCount: Int {
        countLock.lock()
        defer { countLock.unlock() }
        return frames
    }
    private(set) var dropped = 0
    private(set) var scaled = 0
    private var closed = false
    private let width: Int
    private let height: Int
    let onClosed: () -> Void

    // Lazily created when the camera delivers a geometry other than the one
    // requested (another app such as OBS reconfigured the device).
    private var transfer: VTPixelTransferSession?
    private var pool: CVPixelBufferPool?
    private var adaptedFrom = ""

    init(width: Int32, height: Int32, onClosed: @escaping () -> Void) {
        self.width = Int(width)
        self.height = Int(height)
        self.onClosed = onClosed
    }

    /// Scales/converts src into a fresh width x height NV12 buffer.
    private func adapt(_ src: CVPixelBuffer) -> CVPixelBuffer? {
        if transfer == nil {
            var t: VTPixelTransferSession?
            guard VTPixelTransferSessionCreate(allocator: nil, pixelTransferSessionOut: &t) == noErr, let t else { return nil }
            VTSessionSetProperty(t, key: kVTPixelTransferPropertyKey_ScalingMode, value: kVTScalingMode_Letterbox)
            var p: CVPixelBufferPool?
            let attrs: [CFString: Any] = [
                kCVPixelBufferPixelFormatTypeKey: nv12,
                kCVPixelBufferWidthKey: width,
                kCVPixelBufferHeightKey: height,
                kCVPixelBufferIOSurfacePropertiesKey: [:] as CFDictionary,
            ]
            guard CVPixelBufferPoolCreate(nil, nil, attrs as CFDictionary, &p) == kCVReturnSuccess, let p else { return nil }
            transfer = t
            pool = p
        }
        var dst: CVPixelBuffer?
        guard CVPixelBufferPoolCreatePixelBuffer(nil, pool!, &dst) == kCVReturnSuccess, let dst else { return nil }
        guard VTPixelTransferSessionTransferImage(transfer!, from: src, to: dst) == noErr else { return nil }
        return dst
    }

    func captureOutput(_ output: AVCaptureOutput, didOutput sampleBuffer: CMSampleBuffer, from connection: AVCaptureConnection) {
        if closed { return }
        guard var pb = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
        // The consumer (ffmpeg -f rawvideo) assumes a fixed frame size, so a
        // frame of another geometry or pixel format is scaled/converted
        // rather than emitted as-is or dropped.
        let w = CVPixelBufferGetWidth(pb), h = CVPixelBufferGetHeight(pb)
        let fmtType = CVPixelBufferGetPixelFormatType(pb)
        if w != width || h != height || fmtType != nv12 {
            let from = "\(w)x\(h) \(fourCC(fmtType))"
            if from != adaptedFrom {
                adaptedFrom = from
                log("wmd-capture: camera now delivers \(from); scaling to \(width)x\(height) nv12")
            }
            guard let out = adapt(pb) else {
                log("wmd-capture: cannot convert \(from) to \(width)x\(height) nv12; stopping")
                closed = true
                onClosed()
                return
            }
            pb = out
            scaled += 1
        }
        CVPixelBufferLockBaseAddress(pb, .readOnly)
        defer { CVPixelBufferUnlockBaseAddress(pb, .readOnly) }

        // NV12 is bi-planar: plane 0 = Y (w*h), plane 1 = interleaved CbCr (w*h/2).
        // Rows may be padded (bytesPerRow > width), so copy row by row.
        let planes = CVPixelBufferGetPlaneCount(pb)
        for p in 0..<planes {
            guard let base = CVPixelBufferGetBaseAddressOfPlane(pb, p) else { return }
            let rows = CVPixelBufferGetHeightOfPlane(pb, p)
            let rowBytes = CVPixelBufferGetWidthOfPlane(pb, p) * (p == 0 ? 1 : 2)
            let stride = CVPixelBufferGetBytesPerRowOfPlane(pb, p)
            for r in 0..<rows {
                if !writeAll(fd: 1, base + r * stride, rowBytes) {
                    closed = true
                    onClosed()
                    return
                }
            }
        }
        countLock.lock()
        frames += 1
        countLock.unlock()
    }

    func captureOutput(_ output: AVCaptureOutput, didDrop sampleBuffer: CMSampleBuffer, from connection: AVCaptureConnection) {
        dropped += 1
    }
}

/// Watchdog state, touched only on the control queue.
final class Watch {
    var writer: FrameWriter?
    var lastCount = 0
    var lastProgress = Date()
}

func runCapture(_ o: RunOptions) {
    signal(SIGPIPE, SIG_IGN) // detect reader exit via write() error instead of dying

    // Watchdog, started before any AVFoundation call because those can block
    // (for example while a permission prompt is waiting for a click). It
    // stops when the parent pipeline is gone (we were orphaned and no frame
    // came to hit EPIPE), and exits with an error when no frame was written
    // for a while (permission pending, camera unplugged or stalled, or the
    // reader stopped reading), so go2rtc restarts us instead of everyone
    // waiting forever. It exits directly: a graceful stop could hang behind
    // a blocked AVFoundation call or a write() on a full pipe.
    let control = DispatchQueue(label: "wmd-capture.control")
    let watch = Watch()
    let parentAtStart = getppid()
    let watchdog = DispatchSource.makeTimerSource(queue: control)
    watchdog.schedule(deadline: .now() + 1, repeating: 1)
    watchdog.setEventHandler {
        if getppid() != parentAtStart {
            log("wmd-capture: parent process exited; stopping")
            exit(0)
        }
        let n = watch.writer?.frameCount ?? 0
        if n != watch.lastCount {
            watch.lastCount = n
            watch.lastProgress = Date()
        } else if o.stallTimeout > 0 && Date().timeIntervalSince(watch.lastProgress) > o.stallTimeout {
            log(String(format: "wmd-capture: no frames for %.0fs (camera permission pending, or camera stalled); exiting so it is restarted", o.stallTimeout))
            exit(3)
        }
    }
    watchdog.resume()

    guard let dev = videoDevices().first(where: { $0.localizedName == o.device || $0.uniqueID == o.device }) else {
        fail("device not found: \(o.device)")
    }
    log("wmd-capture: device \(dev.localizedName) inUseByAnotherApplication=\(dev.isInUseByAnotherApplication)")

    // Permission belongs to the responsible process: WatchMyDog.app under
    // launchd (ADR-0006), the terminal app when run by hand.
    switch AVCaptureDevice.authorizationStatus(for: .video) {
    case .denied, .restricted:
        fail("camera access denied: allow WatchMyDog in System Settings > Privacy & Security > Camera", code: 4)
    case .notDetermined:
        log("wmd-capture: camera permission not decided yet; macOS is asking on the Mac's screen (click Allow)")
    default:
        break
    }

    // Choose the camera format: 4:2:0 layout, fast enough for the requested
    // fps, then exact size if available, else the smallest size that covers
    // the request, else the largest. Anything other than exact is scaled.
    let wanted: Set<FourCharCode> = [kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange,
                                     kCVPixelFormatType_420YpCbCr8BiPlanarFullRange]
    func dims(_ f: AVCaptureDevice.Format) -> CMVideoDimensions { CMVideoFormatDescriptionGetDimensions(f.formatDescription) }
    let candidates = dev.formats.filter { f in
        wanted.contains(CMFormatDescriptionGetMediaSubType(f.formatDescription))
            && f.videoSupportedFrameRateRanges.contains { $0.maxFrameRate + 0.01 >= o.fps }
    }
    let exact = candidates.first { dims($0).width == o.width && dims($0).height == o.height }
    let covering = candidates.filter { dims($0).width >= o.width && dims($0).height >= o.height }
        .min { dims($0).width * dims($0).height < dims($1).width * dims($1).height }
    let largest = candidates.max { dims($0).width * dims($0).height < dims($1).width * dims($1).height }
    guard let fmt = exact ?? covering ?? largest else {
        fail("no 4:2:0 format at >= \(o.fps) fps on \(dev.localizedName); run `wmd-capture list`")
    }
    let range = fmt.videoSupportedFrameRateRanges.first { $0.maxFrameRate + 0.01 >= o.fps }!
    if exact == nil {
        let d = dims(fmt)
        log("wmd-capture: no native \(o.width)x\(o.height); capturing \(d.width)x\(d.height) and scaling")
    }

    let session = AVCaptureSession()
    guard let input = try? AVCaptureDeviceInput(device: dev), session.canAddInput(input) else {
        fail("cannot add input for \(dev.localizedName)")
    }
    session.addInput(input)

    let output = AVCaptureVideoDataOutput()
    output.videoSettings = [kCVPixelBufferPixelFormatTypeKey as String: nv12]
    output.alwaysDiscardsLateVideoFrames = true
    let done = DispatchSemaphore(value: 0)
    let writer = FrameWriter(width: o.width, height: o.height, onClosed: { done.signal() })
    output.setSampleBufferDelegate(writer, queue: DispatchQueue(label: "wmd-capture.frames"))
    guard session.canAddOutput(output) else { fail("cannot add video output") }
    session.addOutput(output)

    // Signals run on the control queue: the main thread is blocked in
    // done.wait() below, so handlers on .main would never run (an earlier
    // version ignored SIGTERM entirely because of this).
    let stopSignal: (Int32) -> Void = { sig in
        log("wmd-capture: signal \(sig), stopping")
        done.signal()
    }
    signal(SIGINT, SIG_IGN); signal(SIGTERM, SIG_IGN)
    let sigint = DispatchSource.makeSignalSource(signal: SIGINT, queue: control)
    let sigterm = DispatchSource.makeSignalSource(signal: SIGTERM, queue: control)
    sigint.setEventHandler { stopSignal(SIGINT) }; sigint.resume()
    sigterm.setEventHandler { stopSignal(SIGTERM) }; sigterm.resume()

    // Note: isInUseByAnotherApplication stays false for other processes of
    // the same user, so it cannot drive this decision automatically; the
    // caller opts in with --adopt-format when another app should own the
    // camera's configuration (OBS, Camo, ...).
    var adopt = o.adoptFormat
    if adopt {
        log("wmd-capture: --adopt-format: keeping the camera's current format")
    } else {
        // Configure the device and keep the configuration lock held across
        // startRunning(). On macOS 26 the session otherwise reconfigures the
        // device to 1080p when it starts, whatever activeFormat or
        // sessionPreset say (verified empirically, see ADR-0004).
        //
        // The lock is refused (AVError -11817 "Cannot Use <camera>") while
        // another process such as OBS holds it. That is the reliable "someone
        // else owns the camera" signal, so fall back to adopting its format.
        do {
            try dev.lockForConfiguration()
        } catch {
            log("wmd-capture: camera configuration owned by another app (\(error.localizedDescription)); adopting its format")
            adopt = true
        }
    }
    if adopt {
        session.startRunning()
    } else {
        dev.activeFormat = fmt
        // Use the range's own duration: the camera reports 30.00003 fps and
        // rejects an exact 1/30 duration.
        dev.activeVideoMinFrameDuration = range.minFrameDuration
        dev.activeVideoMaxFrameDuration = range.minFrameDuration
        session.startRunning()
        dev.unlockForConfiguration()
    }

    let active = CMVideoFormatDescriptionGetDimensions(dev.activeFormat.formatDescription)
    log("wmd-capture: running \(active.width)x\(active.height) nv12 @ \(range.maxFrameRate) fps")
    watch.writer = writer


    let start = Date()
    if o.seconds > 0 {
        _ = done.wait(timeout: .now() + o.seconds)
    } else {
        done.wait()
    }
    session.stopRunning()
    let elapsed = Date().timeIntervalSince(start)
    log(String(format: "wmd-capture: stopped after %.1fs, %d frames (%.1f fps), %d scaled, %d dropped",
               elapsed, writer.frameCount, Double(writer.frameCount) / max(elapsed, 0.001), writer.scaled, writer.dropped))
}

// MARK: - main

let argv = CommandLine.arguments
switch argv.dropFirst().first {
case "list":
    runList()
case "run":
    runCapture(parseRun(argv.dropFirst(2)))
default:
    log("usage: wmd-capture list | run --device NAME [--width W] [--height H] [--fps F] [--seconds S] [--adopt-format] [--stall-timeout S]")
    exit(2)
}
