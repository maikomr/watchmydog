// Package threshold implements ports.SoundDetector by measuring how loud the
// camera's microphone is and comparing it with a threshold.
//
// Pipeline (ADR-0007):
//
//	go2rtc RTSP (Opus) ──► ffmpeg (decode, mono 16 kHz) ──► pipe of raw
//	samples ──► RMS level per 100 ms window (level.go) ──► state machine
//	(tracker.go) ──► ports.SoundSink
//
// ffmpeg only reads from go2rtc on localhost; it never touches the
// microphone itself, so it needs no macOS permission of its own.
package threshold

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

const (
	// sampleRate is plenty for loudness; howls and barks sit below 4 kHz.
	sampleRate = 16000
	// Window is how much audio one level measurement covers.
	Window = 100 * time.Millisecond
	// windowBytes: 16-bit mono samples in one window.
	windowBytes = sampleRate / 10 * 2
	// warmUp is discarded after every (re)connect: when go2rtc opens the
	// microphone, the first ~300 ms contain a full-scale pop (measured
	// 2026-11-12: -6, -12, -20 dBFS, then the room's -50).
	warmUp = time.Second
)

// Settings say where the audio comes from.
type Settings struct {
	// FFmpegBin is the ffmpeg executable.
	FFmpegBin string
	// RTSPAddr is go2rtc's RTSP listener (host:port).
	RTSPAddr string
}

// openFunc starts delivering a camera's audio as mono 16-bit little-endian
// PCM at sampleRate. Closing the reader stops the source.
type openFunc func(ctx context.Context, cam domain.Camera) (io.ReadCloser, error)

// Detector implements ports.SoundDetector.
type Detector struct {
	rules  Rules
	open   openFunc
	logger *slog.Logger
	now    func() time.Time
	// minBackoff/maxBackoff bound the wait between reconnects.
	minBackoff, maxBackoff time.Duration
}

var _ ports.SoundDetector = (*Detector)(nil)

// New builds a detector that reads audio from go2rtc with ffmpeg.
func New(s Settings, rules Rules, logger *slog.Logger) *Detector {
	return &Detector{
		rules:      rules,
		open:       ffmpegSource(s),
		logger:     logger,
		now:        time.Now,
		minBackoff: time.Second,
		maxBackoff: 5 * time.Second,
	}
}

// Watch implements ports.SoundDetector.
func (d *Detector) Watch(ctx context.Context, cam domain.Camera, sink ports.SoundSink) error {
	if !cam.HasAudio() {
		return fmt.Errorf("camera %s has no audio", cam.ID)
	}
	backoff := d.minBackoff
	listening := false
	for ctx.Err() == nil {
		connected := d.now()
		heard, err := d.listen(ctx, cam, sink, func() {
			listening = true
			d.logger.Info("listening", "camera", cam.ID)
		})
		if ctx.Err() != nil {
			break
		}
		// Log the loss once, not every retry: while the camera is paused
		// this loop fails every few seconds by design.
		if listening || backoff == d.minBackoff {
			d.logger.Warn("audio unavailable, retrying", "camera", cam.ID, "err", err)
		}
		listening = false
		if heard && d.now().Sub(connected) > 2*d.maxBackoff {
			backoff = d.minBackoff
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, d.maxBackoff)
	}
	return nil
}

// listen runs one connection: it reads windows until the source ends and
// reports whether any audio arrived. An event still open when the audio
// stops is ended at the last sound heard.
func (d *Detector) listen(ctx context.Context, cam domain.Camera, sink ports.SoundSink, onFirst func()) (heard bool, err error) {
	src, err := d.open(ctx, cam)
	if err != nil {
		return false, err
	}
	defer src.Close()

	tr := newTracker(d.rules)
	defer func() {
		if sp, open := tr.flush(); open {
			sink.SoundEnded(detection(cam.ID, sp, true))
		}
	}()

	buf := make([]byte, windowBytes)
	var (
		anchor  time.Time // wall-clock time at which the first window was complete
		windows int64
		skipped time.Duration
	)
	for {
		if _, err := io.ReadFull(src, buf); err != nil {
			if closeErr := src.Close(); closeErr != nil {
				err = closeErr
			}
			return heard, err
		}
		if skipped < warmUp {
			skipped += Window
			continue
		}
		if !heard {
			heard = true
			anchor = d.now()
			onFirst()
		}
		// Time comes from counting samples, not from when the bytes were
		// read, so durations stay exact even if the pipe delivers in bursts.
		at := anchor.Add(time.Duration(windows) * Window)
		windows++
		level := rmsDBFS(buf)

		change, sp := tr.observe(at, Window, level)
		switch change {
		case started:
			sink.SoundStarted(detection(cam.ID, sp, false))
		case ended:
			sink.SoundEnded(detection(cam.ID, sp, true))
		}
		sink.SoundLevel(domain.SoundLevel{CameraID: cam.ID, At: at, DBFS: level, Loud: tr.isActive()})
	}
}

func detection(id domain.CameraID, sp span, ended bool) domain.SoundDetection {
	det := domain.SoundDetection{CameraID: id, StartedAt: sp.start, PeakDBFS: sp.peak}
	if ended {
		det.EndedAt = sp.end
	}
	return det
}

// ffmpegSource returns an openFunc that pulls the camera's audio track from
// go2rtc over RTSP and decodes it to raw PCM on a pipe.
//
// "?audio=opus" and -allowed_media_types make it an audio-only viewer, so
// go2rtc starts the microphone but leaves the camera off (the audio source
// is listed first in the generated go2rtc config for the same reason).
func ffmpegSource(s Settings) openFunc {
	return func(ctx context.Context, cam domain.Camera) (io.ReadCloser, error) {
		url := fmt.Sprintf("rtsp://%s/%s?audio=opus", s.RTSPAddr, cam.ID)
		cmd := exec.CommandContext(ctx, s.FFmpegBin,
			"-hide_banner", "-loglevel", "error", "-nostdin",
			"-fflags", "nobuffer", "-flags", "low_delay",
			"-rtsp_transport", "tcp",
			"-timeout", "5000000", // µs without data before ffmpeg gives up
			"-allowed_media_types", "audio",
			"-i", url,
			"-vn", "-ac", "1", "-ar", fmt.Sprint(sampleRate),
			"-f", "s16le", "pipe:1",
		)
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 2 * time.Second
		stderr := &tailBuffer{max: 400}
		cmd.Stderr = stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start ffmpeg: %w", err)
		}
		return &procReader{r: stdout, cmd: cmd, stderr: stderr}, nil
	}
}

// procReader reads a child's stdout; Close stops the child and reports why
// it ended, including the last thing it wrote to stderr.
type procReader struct {
	r      io.Reader
	cmd    *exec.Cmd
	stderr *tailBuffer
	once   sync.Once
	err    error
}

func (p *procReader) Read(b []byte) (int, error) { return p.r.Read(b) }

func (p *procReader) Close() error {
	p.once.Do(func() {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		err := p.cmd.Wait()
		if msg := p.stderr.String(); msg != "" {
			p.err = errors.New("ffmpeg: " + msg)
		} else if err != nil {
			p.err = fmt.Errorf("ffmpeg: %w", err)
		} else {
			p.err = errors.New("ffmpeg: stream ended")
		}
	})
	return p.err
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(t.buf)), " ")
}
