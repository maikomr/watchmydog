// Package memory is a ports.EventStore and ports.SubscriptionStore that
// keeps everything in RAM. It is the
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

// Store implements ports.EventStore and ports.SubscriptionStore.
type Store struct {
	mu     sync.Mutex
	events map[domain.EventID]domain.Event
	subs   []domain.PushSubscription // oldest first
	// FailWith, when set, is returned by SaveEvent and by every
	// subscription method (to test error paths).
	FailWith error
}

var (
	_ ports.EventStore        = (*Store)(nil)
	_ ports.SubscriptionStore = (*Store)(nil)
)

// New returns an empty store.
func New() *Store { return &Store{events: map[domain.EventID]domain.Event{}} }

// SaveSubscription implements ports.SubscriptionStore.
func (s *Store) SaveSubscription(_ context.Context, sub domain.PushSubscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailWith != nil {
		return s.FailWith
	}
	for i, old := range s.subs {
		if old.Endpoint == sub.Endpoint {
			sub.CreatedAt = old.CreatedAt
			s.subs[i] = sub
			return nil
		}
	}
	s.subs = append(s.subs, sub)
	sort.SliceStable(s.subs, func(i, j int) bool { return s.subs[i].CreatedAt.Before(s.subs[j].CreatedAt) })
	return nil
}

// DeleteSubscription implements ports.SubscriptionStore.
func (s *Store) DeleteSubscription(_ context.Context, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailWith != nil {
		return s.FailWith
	}
	for i, old := range s.subs {
		if old.Endpoint == endpoint {
			s.subs = append(s.subs[:i:i], s.subs[i+1:]...)
			break
		}
	}
	return nil
}

// ListSubscriptions implements ports.SubscriptionStore.
func (s *Store) ListSubscriptions(context.Context) ([]domain.PushSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailWith != nil {
		return nil, s.FailWith
	}
	return append([]domain.PushSubscription{}, s.subs...), nil
}

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
