package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

func newService(store ports.EventStore) *Service {
	return New(store, Info{Enabled: true, ThresholdDBFS: -30, ReleaseDBFS: -36}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

var t0 = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func recv(t *testing.T, ch <-chan Message) Message {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return m
	case <-time.After(time.Second):
		t.Fatal("no message")
		return Message{}
	}
}

func TestEventLifecycleIsStoredAndPublished(t *testing.T) {
	store := memory.New()
	s := newService(store)
	ch, cancel := s.Subscribe()
	defer cancel()

	s.SoundStarted(domain.SoundDetection{CameraID: "dog", StartedAt: t0, PeakDBFS: -20})

	started := recv(t, ch).Event
	if started == nil || !started.Ongoing() || started.ID == "" || started.Kind != domain.EventSound {
		t.Fatalf("started message = %+v", started)
	}
	stored, _ := store.ListEvents(context.Background(), ports.EventQuery{})
	if len(stored) != 1 || !stored[0].Ongoing() {
		t.Fatalf("event must be stored as soon as it starts: %+v", stored)
	}
	if on := s.Ongoing(); len(on) != 1 || on[0].ID != started.ID {
		t.Fatalf("Ongoing = %+v", on)
	}

	s.SoundEnded(domain.SoundDetection{CameraID: "dog", StartedAt: t0, EndedAt: t0.Add(4 * time.Second), PeakDBFS: -8})

	ended := recv(t, ch).Event
	if ended == nil || ended.ID != started.ID || ended.Ongoing() || ended.PeakDBFS != -8 {
		t.Fatalf("ended message = %+v", ended)
	}
	stored, _ = store.ListEvents(context.Background(), ports.EventQuery{})
	if len(stored) != 1 || stored[0].Ongoing() || stored[0].EndedAt.Sub(stored[0].StartedAt) != 4*time.Second {
		t.Fatalf("stored event not completed: %+v", stored)
	}
	if len(s.Ongoing()) != 0 {
		t.Error("nothing should be ongoing")
	}
}

func TestLevelsReachSubscribersAndSlowOnesDoNotBlock(t *testing.T) {
	s := newService(memory.New())
	fast, cancelFast := s.Subscribe()
	defer cancelFast()
	_, cancelSlow := s.Subscribe() // never reads
	defer cancelSlow()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range fast {
		}
	}()
	// Far more than the buffer: must not block on the slow subscriber.
	for i := 0; i < 10*subscriberBuffer; i++ {
		s.SoundLevel(domain.SoundLevel{CameraID: "dog", DBFS: -50})
	}
	cancelFast()
	cancelFast() // idempotent
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel must close the channel")
	}
	// Publishing after a cancel must not panic.
	s.SoundLevel(domain.SoundLevel{CameraID: "dog", DBFS: -50})
}

func TestStoreFailureStillPublishes(t *testing.T) {
	store := memory.New()
	store.FailWith = errors.New("disk full")
	s := newService(store)
	ch, cancel := s.Subscribe()
	defer cancel()
	s.SoundStarted(domain.SoundDetection{CameraID: "dog", StartedAt: t0})
	if recv(t, ch).Event == nil {
		t.Fatal("subscribers must get the event even if storing fails")
	}
}

// fakeDetector reports one noise per camera, then waits for ctx.
type fakeDetector struct{}

func (fakeDetector) Watch(ctx context.Context, cam domain.Camera, sink ports.SoundSink) error {
	sink.SoundStarted(domain.SoundDetection{CameraID: cam.ID, StartedAt: t0})
	sink.SoundEnded(domain.SoundDetection{CameraID: cam.ID, StartedAt: t0, EndedAt: t0.Add(time.Second)})
	<-ctx.Done()
	return nil
}

func TestRunWatchesOnlyCamerasWithAudio(t *testing.T) {
	store := memory.New()
	s := newService(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx, fakeDetector{}, []domain.Camera{
			{ID: "dog", Audio: domain.AudioSource{Device: "mic"}},
			{ID: "mute"},
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		got, _ := s.Events(context.Background(), ports.EventQuery{})
		if len(got) == 1 && !got[0].Ongoing() {
			if got[0].CameraID != "dog" {
				t.Fatalf("event from %s", got[0].CameraID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run must return when ctx is done")
	}
}

func TestOnEventStartIsCalledOncePerEventWithTheStoredEvent(t *testing.T) {
	store := memory.New()
	s := newService(store)
	var got []domain.Event
	s.OnEventStart(func(e domain.Event) { got = append(got, e) })

	s.SoundStarted(domain.SoundDetection{CameraID: "dog", StartedAt: t0, PeakDBFS: -20})
	s.SoundEnded(domain.SoundDetection{CameraID: "dog", StartedAt: t0, EndedAt: t0.Add(3 * time.Second), PeakDBFS: -10})

	if len(got) != 1 {
		t.Fatalf("hook called %d times, want 1 (start only)", len(got))
	}
	stored, _ := store.ListEvents(context.Background(), ports.EventQuery{})
	if len(stored) != 1 || stored[0].ID != got[0].ID || got[0].CameraID != "dog" || !got[0].Ongoing() {
		t.Errorf("hook got %+v, stored %+v", got[0], stored)
	}
}

func TestOnEventEndIsCalledOncePerEventWithItsEnd(t *testing.T) {
	s := newService(memory.New())
	var started, ended []domain.Event
	s.OnEventStart(func(e domain.Event) { started = append(started, e) })
	s.OnEventEnd(func(e domain.Event) { ended = append(ended, e) })

	s.SoundStarted(domain.SoundDetection{CameraID: "dog", StartedAt: t0, PeakDBFS: -20})
	if len(ended) != 0 {
		t.Fatal("end hook called at the start")
	}
	end := t0.Add(3 * time.Second)
	s.SoundEnded(domain.SoundDetection{CameraID: "dog", StartedAt: t0, EndedAt: end, PeakDBFS: -10})

	if len(ended) != 1 {
		t.Fatalf("hook called %d times, want 1", len(ended))
	}
	e := ended[0]
	if e.ID != started[0].ID || e.EndedAt == nil || !e.EndedAt.Equal(end) || e.PeakDBFS != -10 {
		t.Errorf("hook got %+v", e)
	}
}
