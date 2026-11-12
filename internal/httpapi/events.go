package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

const (
	defaultEventLimit = 50
	maxEventLimit     = 500
	// sseKeepAlive is how often a comment line is sent when nothing else
	// is, so proxies and the browser keep the connection open.
	sseKeepAlive = 15 * time.Second
)

// eventView is the API shape of an event.
type eventView struct {
	ID        string    `json:"id"`
	CameraID  string    `json:"cameraId"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"startedAt"`
	// EndedAt is null while the event is going on.
	EndedAt  *time.Time `json:"endedAt"`
	PeakDBFS float64    `json:"peakDbfs"`
}

func viewEvent(e domain.Event) eventView {
	return eventView{
		ID: string(e.ID), CameraID: string(e.CameraID), Kind: string(e.Kind),
		StartedAt: e.StartedAt.UTC(), EndedAt: e.EndedAt, PeakDBFS: round1(e.PeakDBFS),
	}
}

func viewEvents(events []domain.Event) []eventView {
	out := make([]eventView, 0, len(events))
	for _, e := range events {
		out = append(out, viewEvent(e))
	}
	return out
}

// levelView is one level-meter reading. Short on purpose: about ten of
// these are sent per second to every open app.
type levelView struct {
	CameraID string `json:"cameraId"`
	// T is the time of the reading in Unix milliseconds.
	T    int64   `json:"t"`
	DBFS float64 `json:"dbfs"`
	// Loud is true while an event is open.
	Loud bool `json:"loud"`
}

// helloView is the first message on the stream: what a client needs to
// draw the meter and the events that are open right now.
type helloView struct {
	Enabled       bool        `json:"enabled"`
	ThresholdDBFS float64     `json:"thresholdDbfs"`
	ReleaseDBFS   float64     `json:"releaseDbfs"`
	Ongoing       []eventView `json:"ongoing"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// handleListEvents serves GET /api/events?camera=&since=&until=&limit=.
// since/until are RFC 3339 times bounding the start of the event.
func (d Deps) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := ports.EventQuery{Limit: defaultEventLimit}
	query := r.URL.Query()
	if v := query.Get("camera"); v != "" {
		if _, ok := d.findCamera(v); !ok {
			writeError(w, http.StatusNotFound, "unknown camera")
			return
		}
		q.CameraID = domain.CameraID(v)
	}
	for name, dst := range map[string]*time.Time{"since": &q.Since, "until": &q.Until} {
		v := query.Get(name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, name+" must be an RFC 3339 time, e.g. 2026-11-12T08:00:00Z")
			return
		}
		*dst = t
	}
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxEventLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be 1-%d", maxEventLimit))
			return
		}
		q.Limit = n
	}
	events, err := d.Monitor.Events(r.Context(), q)
	if err != nil {
		d.Logger.Error("list events", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": viewEvents(events)})
}

// handleEventStream serves GET /api/events/stream?camera= as server-sent
// events (SSE): one long-lived HTTP response on which the server writes
// small text messages. The browser's EventSource reads it and reconnects by
// itself. Messages:
//
//	event: hello   once, first       helloView
//	event: level   ~10 per second    levelView
//	event: sound   on start and end  eventView
func (d Deps) handleEventStream(w http.ResponseWriter, r *http.Request) {
	camera := r.URL.Query().Get("camera")
	if camera != "" {
		if _, ok := d.findCamera(camera); !ok {
			writeError(w, http.StatusNotFound, "unknown camera")
			return
		}
	}
	wants := func(id domain.CameraID) bool { return camera == "" || camera == string(id) }

	// Subscribe before reading the ongoing events so nothing is missed in
	// between; a duplicate is harmless because clients merge by event ID.
	updates, cancel := d.Monitor.Subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // ask buffering proxies to pass messages through
	w.WriteHeader(http.StatusOK)

	// The response writer is wrapped by the access log; ResponseController
	// finds the real one to flush (see statusRecorder.Unwrap).
	rc := http.NewResponseController(w)
	send := func(event string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		return rc.Flush()
	}

	info := d.Monitor.Info()
	hello := helloView{Enabled: info.Enabled, ThresholdDBFS: info.ThresholdDBFS, ReleaseDBFS: info.ReleaseDBFS, Ongoing: []eventView{}}
	for _, e := range d.Monitor.Ongoing() {
		if wants(e.CameraID) {
			hello.Ongoing = append(hello.Ongoing, viewEvent(e))
		}
	}
	if err := send("hello", hello); err != nil {
		return
	}

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done(): // client went away or the server is stopping
			return
		case <-keepAlive.C:
			if _, err = fmt.Fprint(w, ": keep-alive\n\n"); err == nil {
				err = rc.Flush()
			}
		case m := <-updates:
			switch {
			case m.Level != nil && wants(m.Level.CameraID):
				err = send("level", levelView{
					CameraID: string(m.Level.CameraID), T: m.Level.At.UnixMilli(),
					DBFS: round1(m.Level.DBFS), Loud: m.Level.Loud,
				})
			case m.Event != nil && wants(m.Event.CameraID):
				err = send("sound", viewEvent(*m.Event))
			}
		}
		if err != nil {
			return
		}
	}
}
