// watchmydog-launcher: the stable parent process of the watchmydog service.
//
// Why it exists (ADR-0006): macOS camera/microphone permission (TCC) is
// granted to the "responsible" process, which for a launchd agent is the
// program launchd starts. Go binaries get a throwaway ad-hoc signature that
// changes with every build, so starting the service directly would make
// macOS ask for permission again after every update, on the Mac's screen.
// This launcher lives in WatchMyDog.app, changes almost never, and keeps
// the permission; the service and its children (go2rtc, ffmpeg,
// wmd-capture) inherit it as its child processes.
//
// Usage: watchmydog-launcher <program> [args...]
// Runs the program as a child, forwards SIGTERM/SIGINT/SIGHUP to it, and
// exits with its exit status.

import Darwin
import Foundation

let args = Array(CommandLine.arguments.dropFirst())
guard !args.isEmpty else {
    fputs("usage: watchmydog-launcher <program> [args...]\n", stderr)
    exit(2)
}

let forwarded: [Int32] = [SIGTERM, SIGINT, SIGHUP]
var childPID: pid_t = 0
var sources: [DispatchSourceSignal] = []

// Install forwarding before spawning so no signal is lost in between;
// the child gets default handlers back via POSIX_SPAWN_SETSIGDEF.
for sig in forwarded {
    signal(sig, SIG_IGN)
    let src = DispatchSource.makeSignalSource(signal: sig, queue: .main)
    src.setEventHandler {
        if childPID > 0 { kill(childPID, sig) }
    }
    src.resume()
    sources.append(src)
}

var attr: posix_spawnattr_t?
posix_spawnattr_init(&attr)
var defaults = sigset_t()
sigemptyset(&defaults)
for sig in forwarded { sigaddset(&defaults, sig) }
posix_spawnattr_setsigdefault(&attr, &defaults)
posix_spawnattr_setflags(&attr, Int16(POSIX_SPAWN_SETSIGDEF))

var cArgs: [UnsafeMutablePointer<CChar>?] = args.map { strdup($0) } + [nil]
let rc = posix_spawn(&childPID, args[0], nil, &attr, &cArgs, environ)
posix_spawnattr_destroy(&attr)
if rc != 0 {
    fputs("watchmydog-launcher: cannot start \(args[0]): \(String(cString: strerror(rc)))\n", stderr)
    exit(127)
}

DispatchQueue.global().async {
    var status: Int32 = 0
    while waitpid(childPID, &status, 0) == -1 && errno == EINTR {}
    let termsig = status & 0x7f
    exit(termsig == 0 ? (status >> 8) & 0xff : 128 + termsig)
}
dispatchMain()
