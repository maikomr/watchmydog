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
// exits with its exit status. When the child exits, every other process
// left in the launcher's process group (go2rtc, ffmpeg, wmd-capture
// orphans) is stopped first, so nothing keeps holding the camera; launchd
// does not do this for us.

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

/// Other live processes in our process group (we are its leader).
func groupMembers() -> [pid_t] {
    var pids = [pid_t](repeating: 0, count: 4096)
    // Returns the number of PIDs written (not bytes, verified on macOS 26).
    let count = proc_listpgrppids(getpgrp(), &pids, Int32(pids.count * MemoryLayout<pid_t>.size))
    guard count > 0 else { return [] }
    let me = getpid()
    return pids.prefix(Int(count)).filter { $0 > 0 && $0 != me && kill($0, 0) == 0 }
}

DispatchQueue.global().async {
    var status: Int32 = 0
    while waitpid(childPID, &status, 0) == -1 && errno == EINTR {}
    let termsig = status & 0x7f
    let code = termsig == 0 ? (status >> 8) & 0xff : 128 + termsig

    // Only when we lead our own process group (true under launchd). Started
    // by hand from a shell we would share the shell's group: leave it alone.
    var left = getpgrp() == getpid() ? groupMembers() : []
    if !left.isEmpty {
        fputs("watchmydog-launcher: service exited (\(code)); stopping \(left.count) leftover process(es)\n", stderr)
        for pid in left { kill(pid, SIGTERM) }
        for _ in 0..<15 where !left.isEmpty {
            usleep(100_000)
            left = groupMembers()
        }
        for pid in left { kill(pid, SIGKILL) }
    }
    exit(code)
}
dispatchMain()
