package ffplay

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// silent returns a speaker whose "player" is a shell script, so the tests
// make no sound.
func silent(script string, args ...string) *Speaker {
	sp := New(Settings{Bin: "ffplay", Volume: 80}, quiet)
	sp.command = func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", script, "sh"}, args...)...)
	}
	return sp
}

func TestPlayPipesTheStreamToThePlayerInOrder(t *testing.T) {
	out := filepath.Join(t.TempDir(), "heard")
	sp := silent(`cat > "$1"`, out)
	w, err := sp.Play(context.Background(), "dog")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"one ", "two ", "three"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "one two three" {
		t.Errorf("player got %q", got)
	}
}

func TestCloseReportsWhyThePlayerFailed(t *testing.T) {
	sp := silent(`echo "Invalid data found when processing input" >&2; exit 1`)
	w, err := sp.Play(context.Background(), "dog")
	if err != nil {
		t.Fatal(err)
	}
	// The player is gone; sooner or later a write notices.
	deadline := time.Now().Add(5 * time.Second)
	var werr error
	for werr == nil && time.Now().Before(deadline) {
		_, werr = w.Write(make([]byte, 64<<10))
	}
	if werr == nil || !strings.Contains(werr.Error(), "Invalid data found") {
		t.Errorf("write error = %v, want ffplay's message", werr)
	}
	if err := w.Close(); err == nil || !strings.Contains(err.Error(), "Invalid data found") {
		t.Errorf("Close = %v", err)
	}
}

func TestCloseStopsAPlayerThatDoesNotFinish(t *testing.T) {
	sp := silent(`exec sleep 60`) // ignores the end of its input
	sp.drain = 50 * time.Millisecond
	w, err := sp.Play(context.Background(), "dog")
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := w.Close(); err != nil {
		t.Errorf("Close = %v; stopping the player is not an error", err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("Close took %s", took)
	}
}

func TestCancellingTheContextStopsPlayback(t *testing.T) {
	sp := silent(`exec sleep 60`)
	ctx, cancel := context.WithCancel(context.Background())
	w, err := sp.Play(ctx, "dog")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-w.(*playback).done:
	case <-time.After(5 * time.Second):
		t.Fatal("player still running after the context ended")
	}
	_ = w.Close()
}

func TestPlayFailsWhenThePlayerCannotStart(t *testing.T) {
	sp := New(Settings{Bin: "/nonexistent/ffplay"}, quiet)
	if _, err := sp.Play(context.Background(), "dog"); err == nil {
		t.Fatal("no error for a missing ffplay")
	}
}

func TestPlayerCommand(t *testing.T) {
	sp := New(Settings{Bin: "/opt/bin/ffplay", Volume: 70}, quiet)
	args := strings.Join(sp.playerCommand(context.Background()).Args, " ")
	for _, want := range []string{"/opt/bin/ffplay", "-nodisp", "-autoexit", "-volume 70", "-fflags nobuffer", "-probesize 32", "-f matroska -i pipe:0"} {
		if !strings.Contains(args, want) {
			t.Errorf("command lacks %q:\n%s", want, args)
		}
	}
}
