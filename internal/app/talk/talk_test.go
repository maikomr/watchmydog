package talk

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
)

var (
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
	t0    = time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	rules = Settings{Enabled: true, IdleTimeout: 3 * time.Second, MaxDuration: time.Minute, HoldOff: 10 * time.Second}
)

// fakeSpeaker records what each playback was given.
type fakeSpeaker struct {
	mu       sync.Mutex
	plays    []*fakePlayback
	playErr  error
	writeErr error
}

type fakePlayback struct {
	sp     *fakeSpeaker
	cam    domain.CameraID
	heard  strings.Builder
	closed int
	ctx    context.Context
}

func (f *fakeSpeaker) Play(ctx context.Context, cam domain.CameraID) (io.WriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.playErr != nil {
		return nil, f.playErr
	}
	p := &fakePlayback{sp: f, cam: cam, ctx: ctx}
	f.plays = append(f.plays, p)
	return p, nil
}

func (p *fakePlayback) Write(b []byte) (int, error) {
	p.sp.mu.Lock()
	defer p.sp.mu.Unlock()
	if p.sp.writeErr != nil {
		return 0, p.sp.writeErr
	}
	return p.heard.Write(b)
}

func (p *fakePlayback) Close() error {
	p.sp.mu.Lock()
	defer p.sp.mu.Unlock()
	p.closed++
	return nil
}

func (f *fakeSpeaker) play(i int) (heard string, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plays[i].heard.String(), f.plays[i].closed
}

type fixture struct {
	*Service
	sp    *fakeSpeaker
	clock time.Time
	holds map[domain.CameraID]time.Time
}

func newFixture(s Settings) *fixture {
	f := &fixture{sp: &fakeSpeaker{}, clock: t0, holds: map[domain.CameraID]time.Time{}}
	f.Service = New(f.sp, s, quiet)
	f.now = func() time.Time { return f.clock }
	f.OnActive(func(cam domain.CameraID, until time.Time) { f.holds[cam] = until })
	return f
}

func (f *fixture) start(t *testing.T, cam domain.CameraID) SessionID {
	t.Helper()
	id, err := f.Start(cam)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestChunksArePlayedInOrder(t *testing.T) {
	f := newFixture(rules)
	id := f.start(t, "dog")
	for seq, chunk := range []string{"a", "b", "c"} {
		if err := f.Write(id, seq, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	f.Stop(id)
	if heard, closed := f.sp.play(0); heard != "abc" || closed != 1 {
		t.Errorf("heard %q, closed %d times", heard, closed)
	}
	if err := f.Write(id, 3, []byte("d")); !errors.Is(err, ErrNoSession) {
		t.Errorf("write after stop: %v, want ErrNoSession", err)
	}
	f.Stop(id) // stopping again is fine
	if _, closed := f.sp.play(0); closed != 1 {
		t.Errorf("speaker closed %d times", closed)
	}
}

func TestChunksOutOfOrderWaitAndRepeatsAreIgnored(t *testing.T) {
	f := newFixture(rules)
	id := f.start(t, "dog")
	write := func(seq int, chunk string) {
		t.Helper()
		if err := f.Write(id, seq, []byte(chunk)); err != nil {
			t.Fatalf("chunk %d: %v", seq, err)
		}
	}
	write(0, "a")
	write(2, "c") // early: waits for 1
	write(3, "d")
	if heard, _ := f.sp.play(0); heard != "a" {
		t.Fatalf("heard %q before the missing chunk arrived", heard)
	}
	write(1, "b")
	write(1, "b") // the phone repeated the request
	write(0, "a")
	if heard, _ := f.sp.play(0); heard != "abcd" {
		t.Errorf("heard %q, want abcd", heard)
	}
}

func TestTooManyChunksAheadAreRefused(t *testing.T) {
	f := newFixture(rules)
	id := f.start(t, "dog")
	for seq := 1; seq <= maxWaiting; seq++ {
		if err := f.Write(id, seq, []byte("x")); err != nil {
			t.Fatalf("chunk %d: %v", seq, err)
		}
	}
	if err := f.Write(id, maxWaiting+1, []byte("x")); !errors.Is(err, ErrTooFarAhead) {
		t.Errorf("err = %v, want ErrTooFarAhead", err)
	}
}

func TestANewSessionForTheCameraEndsTheOldOne(t *testing.T) {
	f := newFixture(rules)
	first := f.start(t, "dog")
	other := f.start(t, "cat")
	second := f.start(t, "dog")
	if first == second {
		t.Fatal("session IDs repeat")
	}
	if _, closed := f.sp.play(0); closed != 1 {
		t.Error("the first dog session was not ended")
	}
	if _, closed := f.sp.play(1); closed != 0 {
		t.Error("the cat session was ended")
	}
	if err := f.Write(first, 0, []byte("x")); !errors.Is(err, ErrNoSession) {
		t.Errorf("write to the replaced session: %v", err)
	}
	for _, id := range []SessionID{other, second} {
		if err := f.Write(id, 0, []byte("x")); err != nil {
			t.Errorf("write: %v", err)
		}
	}
}

func TestSessionsEndWhenIdleOrTooLong(t *testing.T) {
	f := newFixture(rules) // idle 3 s, longest 1 min
	idle := f.start(t, "dog")
	busy := f.start(t, "cat")
	seq := 0
	for f.clock = t0.Add(2 * time.Second); f.clock.Before(t0.Add(59 * time.Second)); f.clock = f.clock.Add(2 * time.Second) {
		if err := f.Write(busy, seq, []byte("x")); err != nil {
			t.Fatalf("at %s: %v", f.clock.Sub(t0), err)
		}
		seq++
		f.expire(f.clock)
	}
	if err := f.Write(idle, 0, []byte("x")); !errors.Is(err, ErrNoSession) {
		t.Errorf("idle session still open: %v", err)
	}
	if _, closed := f.sp.play(0); closed != 1 {
		t.Error("idle session's speaker not closed")
	}
	f.clock = t0.Add(time.Minute)
	if err := f.Write(busy, seq, []byte("x")); err != nil {
		t.Fatal(err)
	}
	f.expire(f.clock)
	if err := f.Write(busy, seq+1, []byte("x")); !errors.Is(err, ErrNoSession) {
		t.Errorf("session longer than the maximum still open: %v", err)
	}
}

func TestTalkingIsReportedWithAHoldOffTime(t *testing.T) {
	f := newFixture(rules) // hold-off 10 s
	id := f.start(t, "dog")
	if got := f.holds["dog"]; !got.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("after start: held until %v", got)
	}
	f.clock = t0.Add(4 * time.Second)
	if err := f.Write(id, 0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := f.holds["dog"]; !got.Equal(t0.Add(14 * time.Second)) {
		t.Errorf("after a chunk: held until %v", got)
	}
}

func TestSpeakerErrors(t *testing.T) {
	f := newFixture(rules)
	f.sp.playErr = errors.New("no ffplay")
	if _, err := f.Start("dog"); err == nil {
		t.Fatal("Start hid the speaker's error")
	}
	f.sp.playErr = nil

	id := f.start(t, "dog")
	f.sp.writeErr = errors.New("ffplay died")
	if err := f.Write(id, 0, []byte("x")); err == nil || !strings.Contains(err.Error(), "ffplay died") {
		t.Errorf("err = %v", err)
	}
	f.sp.writeErr = nil
	if err := f.Write(id, 1, []byte("x")); !errors.Is(err, ErrNoSession) {
		t.Errorf("session survived a speaker failure: %v", err)
	}
	if _, closed := f.sp.play(0); closed != 1 {
		t.Errorf("speaker closed %d times, want 1", closed)
	}
}

func TestDisabled(t *testing.T) {
	s := rules
	s.Enabled = false
	f := newFixture(s)
	if f.Enabled() {
		t.Error("Enabled() = true")
	}
	if _, err := f.Start("dog"); !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
	// Without a speaker the service is off whatever the settings say.
	if New(nil, rules, quiet).Enabled() {
		t.Error("enabled without a speaker")
	}
	done := make(chan struct{})
	go func() { defer close(done); f.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run must return at once when talking is off")
	}
}

func TestRunEndsStaleSessionsAndAllOfThemAtShutdown(t *testing.T) {
	f := newFixture(rules)
	var mu sync.Mutex
	clock := t0
	f.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.Run(ctx) }()

	stale := f.start(t, "dog")
	mu.Lock()
	clock = t0.Add(5 * time.Second)
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, closed := f.sp.play(0); closed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the idle session was not ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := f.Write(stale, 0, []byte("x")); !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v", err)
	}

	f.start(t, "dog")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if _, closed := f.sp.play(1); closed != 1 {
		t.Error("the open session was not ended at shutdown")
	}
	if f.sp.plays[1].ctx.Err() == nil {
		t.Error("the speaker's context was not cancelled")
	}
}

func TestConcurrentWritesKeepTheOrder(t *testing.T) {
	f := newFixture(rules)
	f.now = time.Now
	id := f.start(t, "dog")
	const n = 200
	var wg sync.WaitGroup
	for seq := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// maxWaiting bounds how far ahead a chunk may be; retry like
			// a client would.
			for {
				err := f.Write(id, seq, []byte{byte('a' + seq%26)})
				if err == nil {
					return
				}
				if !errors.Is(err, ErrTooFarAhead) {
					t.Errorf("chunk %d: %v", seq, err)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	heard, _ := f.sp.play(0)
	if len(heard) != n {
		t.Fatalf("heard %d chunks, want %d", len(heard), n)
	}
	for i := range n {
		if heard[i] != byte('a'+i%26) {
			t.Fatalf("chunk %d out of order: %q", i, heard[:i+1])
		}
	}
}
