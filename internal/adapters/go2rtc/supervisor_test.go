package go2rtc

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeBinary writes a shell script that behaves like a long-running daemon
// unless CRASH_AFTER is set, in which case it exits after that many seconds.
func fakeBinary(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-go2rtc.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitFor(t *testing.T, cond func() bool, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestSupervisorStartAndStop(t *testing.T) {
	bin := fakeBinary(t, `trap 'exit 0' TERM; while true; do sleep 0.1; done`)
	s := newSupervisor(bin, nil, quietLogger())
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if !s.Alive() {
		t.Fatal("expected process alive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Alive() {
		t.Error("expected process stopped")
	}
	// Stop twice is harmless.
	if err := s.Stop(ctx); err != nil {
		t.Error(err)
	}
}

func TestSupervisorRestartsAfterCrash(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs")
	bin := fakeBinary(t, `echo run >> "`+marker+`"; sleep 0.2; exit 1`)
	s := newSupervisor(bin, nil, quietLogger())
	s.minBackoff = 50 * time.Millisecond
	s.maxBackoff = 100 * time.Millisecond
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	ok := waitFor(t, func() bool {
		b, _ := os.ReadFile(marker)
		return len(b) >= len("run\nrun\nrun\n")
	}, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Stop(ctx)
	if !ok {
		t.Fatal("expected at least 3 runs (restart after crash)")
	}
}

func TestSupervisorKillsProcessIgnoringTerm(t *testing.T) {
	bin := fakeBinary(t, `trap '' TERM; while true; do sleep 0.1; done`)
	s := newSupervisor(bin, nil, quietLogger())
	s.stopGrace = 200 * time.Millisecond
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("stop took too long; SIGKILL fallback did not work")
	}
	if s.Alive() {
		t.Error("expected process stopped")
	}
}

func TestSupervisorRestart(t *testing.T) {
	bin := fakeBinary(t, `trap 'exit 0' TERM; while true; do sleep 0.1; done`)
	s := newSupervisor(bin, nil, quietLogger())
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	pid1 := s.cmd.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.Alive() || s.cmd.Process.Pid == pid1 {
		t.Error("expected a new live process after restart")
	}
	_ = s.Stop(ctx)
}

func TestSupervisorStartMissingBinary(t *testing.T) {
	s := newSupervisor("/nonexistent/go2rtc", nil, quietLogger())
	if err := s.Start(); err == nil {
		t.Error("expected error for missing binary")
	}
}
