// Package monitor is the application service for sound monitoring. It sits
// between the ports: it runs a ports.SoundDetector per camera, turns what
// the detector hears into domain.Events, saves them in a ports.EventStore
// and fans levels and events out to live subscribers (the HTTP layer's
// server-sent-events stream; push notifications in a later increment).
//
// It does no I/O of its own, so it is tested with a fake detector and the
// in-memory store.
package monitor

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Info describes the detection setup, for clients that draw a level meter.
type Info struct {
	// Enabled is false when sound detection is switched off in the config.
	Enabled bool
	// ThresholdDBFS is the level that counts as loud.
	ThresholdDBFS float64
	// ReleaseDBFS is the level below which it counts as quiet again.
	ReleaseDBFS float64
}

// Message is one update for a subscriber: exactly one field is set.
type Message struct {
	Level *domain.SoundLevel
	// Event is sent when an event starts and again when it ends.
	Event *domain.Event
}

// subscriberBuffer is how many messages may wait for a slow subscriber
// (about 6 s of levels) before new ones are dropped for it.
const subscriberBuffer = 64

// Service implements ports.SoundSink.
type Service struct {
	store  ports.EventStore
	info   Info
	logger *slog.Logger
	newID  func() domain.EventID

	mu   sync.Mutex
	open map[domain.CameraID]domain.Event
	subs map[chan Message]struct{}
}

var _ ports.SoundSink = (*Service)(nil)

// New builds the service. Nothing runs until Run.
func New(store ports.EventStore, info Info, logger *slog.Logger) *Service {
	return &Service{
		store:  store,
		info:   info,
		logger: logger,
		newID:  domain.NewEventID,
		open:   map[domain.CameraID]domain.Event{},
		subs:   map[chan Message]struct{}{},
	}
}

// Run watches every camera that has audio and blocks until ctx is done and
// all detectors have stopped.
func (s *Service) Run(ctx context.Context, det ports.SoundDetector, cameras []domain.Camera) {
	var wg sync.WaitGroup
	for _, cam := range cameras {
		if !cam.HasAudio() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := det.Watch(ctx, cam, s); err != nil {
				s.logger.Error("sound detector stopped", "camera", cam.ID, "err", err)
			}
		}()
	}
	wg.Wait()
}

// Info returns the detection setup.
func (s *Service) Info() Info { return s.info }

// Events lists stored events, newest first.
func (s *Service) Events(ctx context.Context, q ports.EventQuery) ([]domain.Event, error) {
	return s.store.ListEvents(ctx, q)
}

// Ongoing returns the events that are open right now.
func (s *Service) Ongoing() []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Event, 0, len(s.open))
	for _, e := range s.open {
		out = append(out, e)
	}
	return out
}

// Subscribe returns a channel of live updates and a function that ends the
// subscription (and closes the channel). A subscriber that does not keep up
// misses messages rather than slowing the detector down.
func (s *Service) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, subscriberBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, ch)
			s.mu.Unlock()
			close(ch)
		})
	}
}

// publishLocked must be called with s.mu held, so a subscription cannot be closed
// while we send to it.
func (s *Service) publishLocked(m Message) {
	for ch := range s.subs {
		select {
		case ch <- m:
		default: // subscriber is behind: drop
		}
	}
}

// SoundLevel implements ports.SoundSink.
func (s *Service) SoundLevel(l domain.SoundLevel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) > 0 {
		s.publishLocked(Message{Level: &l})
	}
}

// SoundStarted implements ports.SoundSink: the event is stored right away,
// without an end, so it shows up while the noise is still going on.
func (s *Service) SoundStarted(d domain.SoundDetection) {
	e := domain.Event{
		ID: s.newID(), CameraID: d.CameraID, Kind: domain.EventSound,
		StartedAt: d.StartedAt.UTC(), PeakDBFS: d.PeakDBFS,
	}
	s.mu.Lock()
	s.open[d.CameraID] = e
	s.publishLocked(Message{Event: &e})
	s.mu.Unlock()

	s.logger.Info("sound event started", "camera", e.CameraID, "event", e.ID, "level_dbfs", round1(e.PeakDBFS))
	s.save(e)
}

// SoundEnded implements ports.SoundSink: it completes the stored event.
func (s *Service) SoundEnded(d domain.SoundDetection) {
	end := d.EndedAt.UTC()
	s.mu.Lock()
	e, ok := s.open[d.CameraID]
	if !ok {
		// An end without a start should not happen; keep the data anyway.
		e = domain.Event{ID: s.newID(), CameraID: d.CameraID, Kind: domain.EventSound, StartedAt: d.StartedAt.UTC()}
	}
	delete(s.open, d.CameraID)
	e.EndedAt = &end
	e.PeakDBFS = d.PeakDBFS
	s.publishLocked(Message{Event: &e})
	s.mu.Unlock()

	s.logger.Info("sound event ended", "camera", e.CameraID, "event", e.ID,
		"duration", end.Sub(e.StartedAt).Round(100*time.Millisecond), "peak_dbfs", round1(e.PeakDBFS))
	s.save(e)
}

func (s *Service) save(e domain.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.SaveEvent(ctx, e); err != nil {
		// Live subscribers already have it; only the history misses it.
		s.logger.Error("could not store event", "event", e.ID, "err", err)
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
