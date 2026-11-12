package threshold

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
)

// recorder is a ports.SoundSink that remembers everything.
type recorder struct {
	mu      sync.Mutex
	levels  []domain.SoundLevel
	started []domain.SoundDetection
	ended   []domain.SoundDetection
}

func (r *recorder) SoundLevel(l domain.SoundLevel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels = append(r.levels, l)
}
func (r *recorder) SoundStarted(d domain.SoundDetection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, d)
}
func (r *recorder) SoundEnded(d domain.SoundDetection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = append(r.ended, d)
}

var cam = domain.Camera{ID: "dog", Audio: domain.AudioSource{Device: "mic"}}

// seconds of square-wave PCM at the given amplitude.
func audio(d time.Duration, amp int16) []byte {
	return pcm(int(d.Seconds()*sampleRate), amp)
}

const (
	quietAmp = 100   // about -50 dBFS
	loudAmp  = 10000 // about -10 dBFS
)

// testDetector reads the given streams one after another (one per
// connection); after the last one, opening fails and cancels the context.
func testDetector(t *testing.T, cancel context.CancelFunc, streams ...[]byte) *Detector {
	t.Helper()
	var mu sync.Mutex
	next := 0
	t0 := time.Unix(5000, 0)
	return &Detector{
		rules:      testRules,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:        func() time.Time { return t0 },
		minBackoff: time.Millisecond,
		maxBackoff: time.Millisecond,
		open: func(context.Context, domain.Camera) (io.ReadCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			if next >= len(streams) {
				cancel()
				return nil, errors.New("no more streams")
			}
			s := streams[next]
			next++
			return io.NopCloser(bytes.NewReader(s)), nil
		},
	}
}

func TestWatchDetectsNoiseInPCM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := bytes.Join([][]byte{
		audio(warmUp, 32767), // the microphone's start-up pop must be ignored
		audio(2*time.Second, quietAmp),
		audio(3*time.Second, loudAmp),
		audio(4*time.Second, quietAmp),
	}, nil)
	rec := &recorder{}
	if err := testDetector(t, cancel, stream).Watch(ctx, cam, rec); err != nil {
		t.Fatal(err)
	}

	if len(rec.started) != 1 || len(rec.ended) != 1 {
		t.Fatalf("want 1 started and 1 ended, got %d and %d", len(rec.started), len(rec.ended))
	}
	t0 := time.Unix(5000, 0)
	ev := rec.ended[0]
	// The clock is read when the first 100 ms window is complete, so the
	// audio itself began 100 ms before t0: noise from 1.9 s to 4.9 s.
	if !ev.StartedAt.Equal(t0.Add(1900*time.Millisecond)) || !ev.EndedAt.Equal(t0.Add(4900*time.Millisecond)) {
		t.Errorf("event %v .. %v, want +1.9s .. +4.9s", ev.StartedAt.Sub(t0), ev.EndedAt.Sub(t0))
	}
	if !rec.levels[0].At.Equal(t0) {
		t.Errorf("first level at %v, want t0", rec.levels[0].At.Sub(t0))
	}
	if ev.PeakDBFS < -11 || ev.PeakDBFS > -10 {
		t.Errorf("peak = %.1f, want about -10.3", ev.PeakDBFS)
	}
	if !rec.started[0].EndedAt.IsZero() {
		t.Error("started detection must not have an end")
	}
	if len(rec.levels) != 90 {
		t.Errorf("levels = %d, want 90 (9 s after warm-up)", len(rec.levels))
	}
	loud := 0
	for _, l := range rec.levels {
		if l.Loud {
			loud++
		}
	}
	// Open from the window that completes 1 s of noise (21 loud windows
	// left, including it) until 3 s of quiet; the 30th quiet window closes it.
	if loud != 50 {
		t.Errorf("loud windows = %d, want 50", loud)
	}
}

func TestWatchEndsOpenEventWhenAudioIsLostAndReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := append(audio(warmUp, 0), audio(2*time.Second, loudAmp)...) // cut off mid-noise
	second := append(audio(warmUp, 0), audio(time.Second, quietAmp)...)
	rec := &recorder{}
	if err := testDetector(t, cancel, first, second).Watch(ctx, cam, rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.started) != 1 || len(rec.ended) != 1 {
		t.Fatalf("want the open event ended on loss, got %d started, %d ended", len(rec.started), len(rec.ended))
	}
	if d := rec.ended[0].EndedAt.Sub(rec.ended[0].StartedAt); d != 2*time.Second {
		t.Errorf("event length = %v, want 2s", d)
	}
	if len(rec.levels) != 30 {
		t.Errorf("levels = %d, want 30 (both connections)", len(rec.levels))
	}
}

func TestWatchRejectsCameraWithoutAudio(t *testing.T) {
	d := testDetector(t, func() {})
	if err := d.Watch(context.Background(), domain.Camera{ID: "mute"}, &recorder{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 10}
	b.Write([]byte("0123456789"))
	b.Write([]byte("ab\ncd"))
	if got := b.String(); got != "56789ab cd" {
		t.Errorf("got %q", got)
	}
}
