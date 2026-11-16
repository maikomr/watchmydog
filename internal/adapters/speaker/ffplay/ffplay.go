// Package ffplay implements ports.Speaker by piping the voice stream into
// ffplay, ffmpeg's player, which decodes it and plays it on the Mac's
// current audio output (ADR-0010).
//
// One ffplay process lives for one stretch of talking. Playing sound needs
// no macOS permission.
package ffplay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Settings configure the speaker.
type Settings struct {
	// Bin is the ffplay executable.
	Bin string
	// Volume is ffplay's own volume, 0 to 100. The Mac's output volume
	// applies on top of it.
	Volume int
}

// Speaker implements ports.Speaker.
type Speaker struct {
	s      Settings
	logger *slog.Logger
	// command builds the player process; replaced in tests.
	command func(ctx context.Context) *exec.Cmd
	// drain is how long Close waits for the player to finish what it has.
	drain time.Duration
}

var _ ports.Speaker = (*Speaker)(nil)

// New builds the speaker. Nothing runs until Play.
func New(s Settings, logger *slog.Logger) *Speaker {
	sp := &Speaker{s: s, logger: logger, drain: 2 * time.Second}
	sp.command = sp.playerCommand
	return sp
}

// playerCommand reads a WebM stream from stdin and plays it with as little
// buffering as ffplay allows.
func (sp *Speaker) playerCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, sp.s.Bin,
		"-hide_banner", "-loglevel", "error",
		"-nodisp",   // no window
		"-autoexit", // quit when the stream ends
		"-volume", strconv.Itoa(sp.s.Volume),
		// Start playing at once instead of first reading ahead to learn
		// about the stream; we say what it is (-f matroska covers WebM).
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-probesize", "32", "-analyzeduration", "0",
		"-f", "matroska", "-i", "pipe:0",
	)
}

// Play implements ports.Speaker.
func (sp *Speaker) Play(ctx context.Context, cam domain.CameraID) (io.WriteCloser, error) {
	cmd := sp.command(ctx)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	stderr := &strings.Builder{}
	cmd.Stderr = &limitedWriter{w: stderr, left: 400}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("speaker: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("speaker: start ffplay: %w", err)
	}
	p := &playback{sp: sp, cam: cam, cmd: cmd, stdin: stdin, stderr: stderr, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// playback is one running ffplay.
type playback struct {
	sp      *Speaker
	cam     domain.CameraID
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *strings.Builder
	done    chan struct{}
	waitErr error
	once    sync.Once
	err     error
}

func (p *playback) Write(b []byte) (int, error) {
	n, err := p.stdin.Write(b)
	if err != nil {
		// The pipe broke because ffplay ended; say why.
		return n, p.Close()
	}
	return n, nil
}

// Close ends the stream. ffplay plays what it still has and exits; if it
// does not within the drain time it is stopped.
func (p *playback) Close() error {
	p.once.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-time.After(p.sp.drain):
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(time.Second):
				_ = p.cmd.Process.Kill()
				<-p.done
			}
			return // stopping it ourselves is not an error
		}
		if msg := strings.Join(strings.Fields(p.stderr.String()), " "); msg != "" {
			p.err = errors.New("speaker: ffplay: " + msg)
		} else if p.waitErr != nil {
			p.err = fmt.Errorf("speaker: ffplay: %w", p.waitErr)
		}
	})
	return p.err
}

// limitedWriter keeps the first bytes written to it and drops the rest.
type limitedWriter struct {
	mu   sync.Mutex
	w    io.Writer
	left int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.left > 0 {
		part := b[:min(len(b), l.left)]
		l.left -= len(part)
		_, _ = l.w.Write(part)
	}
	return len(b), nil
}
