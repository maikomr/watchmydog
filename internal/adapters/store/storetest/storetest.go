// Package storetest is the contract every ports.EventStore must fulfil. Each
// adapter's test calls Run, so SQLite, the in-memory fake and any future
// store (Postgres) are held to the same behaviour.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Run exercises a fresh, empty store.
func Run(t *testing.T, newStore func(t *testing.T) ports.EventStore) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	ev := func(id string, cam domain.CameraID, start time.Duration, length time.Duration) domain.Event {
		e := domain.Event{
			ID: domain.EventID(id), CameraID: cam, Kind: domain.EventSound,
			StartedAt: t0.Add(start), PeakDBFS: -12.5,
		}
		if length >= 0 {
			end := e.StartedAt.Add(length)
			e.EndedAt = &end
		}
		return e
	}
	ids := func(events []domain.Event) []string {
		out := make([]string, len(events))
		for i, e := range events {
			out[i] = string(e.ID)
		}
		return out
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	list := func(t *testing.T, s ports.EventStore, q ports.EventQuery, want ...string) []domain.Event {
		t.Helper()
		got, err := s.ListEvents(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if !equal(ids(got), want) {
			t.Fatalf("query %+v: got %v, want %v", q, ids(got), want)
		}
		return got
	}

	t.Run("empty store lists nothing", func(t *testing.T) {
		s := newStore(t)
		got := list(t, s, ports.EventQuery{})
		if got == nil {
			t.Error("want an empty slice, not nil (it is encoded as JSON)")
		}
	})

	t.Run("round trip keeps every field", func(t *testing.T) {
		s := newStore(t)
		in := ev("a", "dog", 0, 4500*time.Millisecond)
		if err := s.SaveEvent(ctx, in); err != nil {
			t.Fatal(err)
		}
		got := list(t, s, ports.EventQuery{}, "a")[0]
		if got.CameraID != in.CameraID || got.Kind != in.Kind || got.PeakDBFS != in.PeakDBFS ||
			!got.StartedAt.Equal(in.StartedAt) || got.EndedAt == nil || !got.EndedAt.Equal(*in.EndedAt) {
			t.Errorf("got %+v, want %+v", got, in)
		}
	})

	t.Run("saving the same id replaces the event", func(t *testing.T) {
		s := newStore(t)
		open := ev("a", "dog", 0, -1)
		if err := s.SaveEvent(ctx, open); err != nil {
			t.Fatal(err)
		}
		if got := list(t, s, ports.EventQuery{}, "a")[0]; !got.Ongoing() {
			t.Error("event should be ongoing")
		}
		closed := ev("a", "dog", 0, 3*time.Second)
		closed.PeakDBFS = -3
		if err := s.SaveEvent(ctx, closed); err != nil {
			t.Fatal(err)
		}
		got := list(t, s, ports.EventQuery{}, "a")[0]
		if got.Ongoing() || got.PeakDBFS != -3 {
			t.Errorf("event not replaced: %+v", got)
		}
	})

	t.Run("newest first, filters and limit", func(t *testing.T) {
		s := newStore(t)
		for _, e := range []domain.Event{
			ev("b", "dog", 2*time.Hour, time.Second),
			ev("a", "dog", 1*time.Hour, time.Second),
			ev("c", "dog", 3*time.Hour, time.Second),
			ev("x", "cat", 90*time.Minute, time.Second),
		} {
			if err := s.SaveEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		list(t, s, ports.EventQuery{}, "c", "b", "x", "a")
		list(t, s, ports.EventQuery{CameraID: "dog"}, "c", "b", "a")
		list(t, s, ports.EventQuery{Limit: 2}, "c", "b")
		// Since is inclusive, Until is exclusive.
		list(t, s, ports.EventQuery{Since: t0.Add(2 * time.Hour)}, "c", "b")
		list(t, s, ports.EventQuery{Until: t0.Add(2 * time.Hour)}, "x", "a")
		list(t, s, ports.EventQuery{CameraID: "dog", Since: t0.Add(time.Hour), Until: t0.Add(3 * time.Hour)}, "b", "a")
	})

	t.Run("close open events", func(t *testing.T) {
		s := newStore(t)
		for _, e := range []domain.Event{ev("done", "dog", 0, time.Second), ev("cut", "dog", time.Hour, -1)} {
			if err := s.SaveEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.CloseOpenEvents(ctx)
		if err != nil || n != 1 {
			t.Fatalf("closed %d, err %v; want 1", n, err)
		}
		got := list(t, s, ports.EventQuery{}, "cut", "done")
		if got[0].Ongoing() || !got[0].EndedAt.Equal(got[0].StartedAt) {
			t.Errorf("cut event should end at its start: %+v", got[0])
		}
		if d := got[1].EndedAt.Sub(got[1].StartedAt); d != time.Second {
			t.Errorf("finished event changed: %v", d)
		}
		if n, _ := s.CloseOpenEvents(ctx); n != 0 {
			t.Errorf("second call closed %d", n)
		}
	})
}
