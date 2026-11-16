// Package storetest is the contract every ports.EventStore (Run),
// ports.SubscriptionStore (RunSubscriptions), ports.PauseStore (RunPauses)
// and ports.ClipStore (RunClips) must fulfil. Each
// adapter's test calls Run, so SQLite, the in-memory fake and any future
// store (Postgres) are held to the same behaviour.
package storetest

import (
	"context"
	"errors"
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

// RunSubscriptions exercises a fresh, empty subscription store.
func RunSubscriptions(t *testing.T, newStore func(t *testing.T) ports.SubscriptionStore) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	sub := func(name string, age time.Duration) domain.PushSubscription {
		return domain.PushSubscription{
			Endpoint: "https://push.example/" + name, P256DH: "key-" + name, Auth: "auth-" + name,
			CreatedAt: t0.Add(age),
		}
	}
	list := func(t *testing.T, s ports.SubscriptionStore) []domain.PushSubscription {
		t.Helper()
		got, err := s.ListSubscriptions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("empty store lists nothing, not nil", func(t *testing.T) {
		if got := list(t, newStore(t)); got == nil || len(got) != 0 {
			t.Fatalf("got %#v, want empty non-nil slice", got)
		}
	})

	t.Run("save and list oldest first", func(t *testing.T) {
		s := newStore(t)
		for _, x := range []domain.PushSubscription{sub("b", time.Minute), sub("a", 0)} {
			if err := s.SaveSubscription(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		got := list(t, s)
		if len(got) != 2 || got[0] != sub("a", 0) || got[1] != sub("b", time.Minute) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("saving the same endpoint replaces keys and keeps the creation time", func(t *testing.T) {
		s := newStore(t)
		if err := s.SaveSubscription(ctx, sub("a", 0)); err != nil {
			t.Fatal(err)
		}
		again := sub("a", time.Hour)
		again.P256DH, again.Auth = "new-key", "new-auth"
		if err := s.SaveSubscription(ctx, again); err != nil {
			t.Fatal(err)
		}
		got := list(t, s)
		if len(got) != 1 {
			t.Fatalf("got %d subscriptions, want 1", len(got))
		}
		if got[0].P256DH != "new-key" || got[0].Auth != "new-auth" || !got[0].CreatedAt.Equal(t0) {
			t.Errorf("got %+v", got[0])
		}
	})

	t.Run("delete removes only that endpoint; unknown is fine", func(t *testing.T) {
		s := newStore(t)
		for _, x := range []domain.PushSubscription{sub("a", 0), sub("b", time.Minute)} {
			if err := s.SaveSubscription(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteSubscription(ctx, sub("a", 0).Endpoint); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSubscription(ctx, "https://push.example/never-seen"); err != nil {
			t.Errorf("deleting an unknown endpoint: %v", err)
		}
		got := list(t, s)
		if len(got) != 1 || got[0].Endpoint != sub("b", 0).Endpoint {
			t.Fatalf("got %+v", got)
		}
	})
}

// RunPauses exercises a fresh, empty pause store.
func RunPauses(t *testing.T, newStore func(t *testing.T) ports.PauseStore) {
	ctx := context.Background()
	check := func(t *testing.T, s ports.PauseStore, want ...domain.CameraID) {
		t.Helper()
		got, err := s.PausedCameras(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got) != len(want) {
			t.Fatalf("got %#v, want %v (non-nil)", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
	set := func(t *testing.T, s ports.PauseStore, id domain.CameraID, paused bool) {
		t.Helper()
		if err := s.SetPaused(ctx, id, paused); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("empty store lists nothing, not nil", func(t *testing.T) {
		check(t, newStore(t))
	})

	t.Run("pause, list sorted, resume", func(t *testing.T) {
		s := newStore(t)
		set(t, s, "dog", true)
		set(t, s, "cat", true)
		check(t, s, "cat", "dog")
		set(t, s, "cat", false)
		check(t, s, "dog")
	})

	t.Run("repeating a call changes nothing", func(t *testing.T) {
		s := newStore(t)
		set(t, s, "dog", true)
		set(t, s, "dog", true)
		check(t, s, "dog")
		set(t, s, "dog", false)
		set(t, s, "dog", false)
		set(t, s, "never-paused", false)
		check(t, s)
	})
}

// RunClips exercises a fresh, empty clip store.
func RunClips(t *testing.T, newStore func(t *testing.T) ports.ClipStore) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	clip := func(id string, cam domain.CameraID, age time.Duration) domain.Clip {
		return domain.Clip{
			ID: domain.ClipID(id), CameraID: cam, StartedAt: t0.Add(age),
			Duration: 42500 * time.Millisecond, SizeBytes: 5_000_000_000, // more than 32 bits
		}
	}
	fill := func(t *testing.T, s ports.ClipStore, clips ...domain.Clip) {
		t.Helper()
		for _, c := range clips {
			if err := s.SaveClip(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	list := func(t *testing.T, s ports.ClipStore, cam domain.CameraID, limit int, want ...string) {
		t.Helper()
		got, err := s.ListClips(ctx, cam, limit)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got) != len(want) {
			t.Fatalf("camera %q limit %d: got %+v, want %v (non-nil)", cam, limit, got, want)
		}
		for i := range want {
			if string(got[i].ID) != want[i] {
				t.Fatalf("camera %q limit %d: got %+v, want %v", cam, limit, got, want)
			}
		}
	}

	t.Run("empty store", func(t *testing.T) {
		s := newStore(t)
		list(t, s, "", 0)
		if _, err := s.GetClip(ctx, "nope"); !errors.Is(err, ports.ErrClipNotFound) {
			t.Errorf("err = %v, want ErrClipNotFound", err)
		}
	})

	t.Run("save, get and list newest first", func(t *testing.T) {
		s := newStore(t)
		fill(t, s, clip("a", "dog", 0), clip("c", "dog", 2*time.Minute), clip("b", "cat", time.Minute))
		got, err := s.GetClip(ctx, "a")
		if err != nil || got != clip("a", "dog", 0) {
			t.Fatalf("got %+v, %v", got, err)
		}
		list(t, s, "", 0, "c", "b", "a")
		list(t, s, "dog", 0, "c", "a")
		list(t, s, "", 2, "c", "b")
		list(t, s, "bird", 0)
	})

	t.Run("saving the same id replaces", func(t *testing.T) {
		s := newStore(t)
		fill(t, s, clip("a", "dog", 0))
		changed := clip("a", "dog", time.Hour)
		changed.SizeBytes = 7
		fill(t, s, changed)
		list(t, s, "", 0, "a")
		if got, _ := s.GetClip(ctx, "a"); got != changed {
			t.Errorf("got %+v, want %+v", got, changed)
		}
	})

	t.Run("delete removes only that clip; unknown is fine", func(t *testing.T) {
		s := newStore(t)
		fill(t, s, clip("a", "dog", 0), clip("b", "dog", time.Minute))
		if err := s.DeleteClip(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteClip(ctx, "never-seen"); err != nil {
			t.Errorf("deleting an unknown clip: %v", err)
		}
		list(t, s, "", 0, "b")
	})
}
