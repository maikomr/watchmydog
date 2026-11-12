package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/app/monitor"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

func newMonitor(store ports.EventStore) *monitor.Service {
	return monitor.New(store, monitor.Info{Enabled: true, ThresholdDBFS: -30, ReleaseDBFS: -36},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func eventsHandler(mon *monitor.Service) http.Handler {
	return NewHandler(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cameras: []domain.Camera{dog()},
		Monitor: mon,
	})
}

var evT0 = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func seedEvents(t *testing.T, store ports.EventStore) {
	t.Helper()
	end := evT0.Add(4 * time.Second)
	for _, e := range []domain.Event{
		{ID: "old", CameraID: "dog", Kind: domain.EventSound, StartedAt: evT0, EndedAt: &end, PeakDBFS: -12.34},
		{ID: "new", CameraID: "dog", Kind: domain.EventSound, StartedAt: evT0.Add(time.Hour), PeakDBFS: -5},
	} {
		if err := store.SaveEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

type eventsBody struct {
	Events []struct {
		ID        string     `json:"id"`
		CameraID  string     `json:"cameraId"`
		Kind      string     `json:"kind"`
		StartedAt time.Time  `json:"startedAt"`
		EndedAt   *time.Time `json:"endedAt"`
		PeakDBFS  float64    `json:"peakDbfs"`
	} `json:"events"`
}

func TestListEvents(t *testing.T) {
	store := memory.New()
	seedEvents(t, store)
	h := eventsHandler(newMonitor(store))

	rr := do(h, http.MethodGet, "/api/events")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}
	var body eventsBody
	decode(t, rr, &body)
	if len(body.Events) != 2 || body.Events[0].ID != "new" || body.Events[1].ID != "old" {
		t.Fatalf("want newest first, got %+v", body.Events)
	}
	if body.Events[0].EndedAt != nil {
		t.Error("ongoing event must have endedAt null")
	}
	old := body.Events[1]
	if old.CameraID != "dog" || old.Kind != "sound" || !old.StartedAt.Equal(evT0) ||
		old.EndedAt == nil || old.EndedAt.Sub(old.StartedAt) != 4*time.Second || old.PeakDBFS != -12.3 {
		t.Errorf("event = %+v", old)
	}

	for path, want := range map[string]string{
		"/api/events?limit=1":                           "new",
		"/api/events?until=2026-11-12T10:30:00Z":        "old",
		"/api/events?since=2026-11-12T10:30:00Z":        "new",
		"/api/events?camera=dog&limit=1":                "new",
		"/api/events?since=2026-11-12T12:30:00%2B02:00": "new", // 10:30 UTC with an offset
	} {
		var b eventsBody
		decode(t, do(h, http.MethodGet, path), &b)
		if len(b.Events) != 1 || b.Events[0].ID != want {
			t.Errorf("%s: got %+v, want only %s", path, b.Events, want)
		}
	}
}

func TestListEventsEmptyIsAnArray(t *testing.T) {
	rr := do(eventsHandler(newMonitor(memory.New())), http.MethodGet, "/api/events")
	if got := strings.TrimSpace(rr.Body.String()); got != `{"events":[]}` {
		t.Errorf("body = %s", got)
	}
}

func TestListEventsRejectsBadParameters(t *testing.T) {
	h := eventsHandler(newMonitor(memory.New()))
	for path, want := range map[string]int{
		"/api/events?limit=0":         http.StatusBadRequest,
		"/api/events?limit=501":       http.StatusBadRequest,
		"/api/events?limit=abc":       http.StatusBadRequest,
		"/api/events?since=yesterday": http.StatusBadRequest,
		"/api/events?until=12:00":     http.StatusBadRequest,
		"/api/events?camera=cat":      http.StatusNotFound,
		"/api/events/stream?camera=x": http.StatusNotFound,
	} {
		if rr := do(h, http.MethodGet, path); rr.Code != want {
			t.Errorf("%s: status %d, want %d", path, rr.Code, want)
		}
	}
}

func TestEventsRoutesAbsentWithoutMonitor(t *testing.T) {
	h := newTest(t, newEngine(), "")
	if rr := do(h, http.MethodGet, "/api/events"); rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// sseMessage is one parsed server-sent event.
type sseMessage struct {
	event string
	data  string
}

// readSSE parses messages from an event stream until it ends.
func readSSE(r io.Reader, out chan<- sseMessage) {
	defer close(out)
	sc := bufio.NewScanner(r)
	var m sseMessage
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			m.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			m.data = strings.TrimPrefix(line, "data: ")
		case line == "" && m.event != "":
			out <- m
			m = sseMessage{}
		}
	}
}

func nextSSE(t *testing.T, ch <-chan sseMessage, wantEvent string, v any) {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatalf("stream ended, wanted %s", wantEvent)
		}
		if m.event != wantEvent {
			t.Fatalf("got event %q (%s), want %q", m.event, m.data, wantEvent)
		}
		if err := json.Unmarshal([]byte(m.data), v); err != nil {
			t.Fatalf("decode %q: %v", m.data, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", wantEvent)
	}
}

func TestEventStream(t *testing.T) {
	mon := newMonitor(memory.New())
	// An event that is already going on when the client connects.
	mon.SoundStarted(domain.SoundDetection{CameraID: "dog", StartedAt: evT0, PeakDBFS: -20})

	// A real server: the stream must be flushed message by message through
	// the access-log wrapper, which a ResponseRecorder would not show.
	srv := httptest.NewServer(eventsHandler(mon))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events/stream?camera=dog", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	msgs := make(chan sseMessage, 16)
	go readSSE(res.Body, msgs)

	var hello struct {
		Enabled       bool
		ThresholdDbfs float64
		ReleaseDbfs   float64
		Ongoing       []struct{ ID string }
	}
	nextSSE(t, msgs, "hello", &hello)
	if !hello.Enabled || hello.ThresholdDbfs != -30 || hello.ReleaseDbfs != -36 || len(hello.Ongoing) != 1 {
		t.Fatalf("hello = %+v", hello)
	}

	// Another camera's level is filtered out; the dog's arrives.
	mon.SoundLevel(domain.SoundLevel{CameraID: "cat", At: evT0, DBFS: -1})
	mon.SoundLevel(domain.SoundLevel{CameraID: "dog", At: evT0.Add(time.Second), DBFS: -47.26, Loud: true})
	var level struct {
		CameraID string
		T        int64
		Dbfs     float64
		Loud     bool
	}
	nextSSE(t, msgs, "level", &level)
	if level.CameraID != "dog" || level.Dbfs != -47.3 || !level.Loud || level.T != evT0.Add(time.Second).UnixMilli() {
		t.Fatalf("level = %+v", level)
	}

	mon.SoundEnded(domain.SoundDetection{CameraID: "dog", StartedAt: evT0, EndedAt: evT0.Add(3 * time.Second), PeakDBFS: -9})
	var ev struct {
		ID       string
		EndedAt  *time.Time
		PeakDbfs float64
	}
	nextSSE(t, msgs, "sound", &ev)
	if ev.ID != hello.Ongoing[0].ID || ev.EndedAt == nil || ev.PeakDbfs != -9 {
		t.Fatalf("sound = %+v", ev)
	}

	// Closing the request ends the handler (the stream closes).
	cancel()
	select {
	case _, ok := <-msgs:
		if ok {
			t.Error("unexpected message after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not end")
	}
}
