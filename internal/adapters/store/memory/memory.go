// Package memory is a ports.EventStore that keeps events in RAM. It is the
// fake used by tests of everything above the store; nothing survives a
// restart.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Store implements ports.EventStore.
type Store struct {
	mu     sync.Mutex
	events map[domain.EventID]domain.Event
	// FailWith, when set, is returned by SaveEvent (to test error paths).
	FailWith error
}

var _ ports.EventStore = (*Store)(nil)

// New returns an empty store.
func New() *Store { return &Store{events: map[domain.EventID]domain.Event{}} }

// SaveEvent implements ports.EventStore.
func (s *Store) SaveEvent(_ context.Context, e domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailWith != nil {
		return s.FailWith
	}
	s.events[e.ID] = clone(e)
	return nil
}

// ListEvents implements ports.EventStore.
func (s *Store) ListEvents(_ context.Context, q ports.EventQuery) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.Event{}
	for _, e := range s.events {
		if q.CameraID != "" && e.CameraID != q.CameraID {
			continue
		}
		if !q.Since.IsZero() && e.StartedAt.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && !e.StartedAt.Before(q.Until) {
			continue
		}
		out = append(out, clone(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	limit := q.Limit
	if limit <= 0 {
		limit = ports.DefaultEventLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CloseOpenEvents implements ports.EventStore.
func (s *Store) CloseOpenEvents(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, e := range s.events {
		if e.EndedAt == nil {
			end := e.StartedAt
			e.EndedAt = &end
			s.events[id] = e
			n++
		}
	}
	return n, nil
}

// Close implements ports.EventStore.
func (s *Store) Close() error { return nil }

// clone copies the event so callers cannot change stored data through the
// EndedAt pointer.
func clone(e domain.Event) domain.Event {
	if e.EndedAt != nil {
		end := *e.EndedAt
		e.EndedAt = &end
	}
	return e
}
