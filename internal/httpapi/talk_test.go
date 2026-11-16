package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/app/talk"
	"github.com/maikomr/watchmydog/internal/domain"
)

// memSpeaker keeps what was "played" in memory.
type memSpeaker struct {
	mu     sync.Mutex
	heard  bytes.Buffer
	closed int
	err    error
}

func (m *memSpeaker) Play(context.Context, domain.CameraID) (io.WriteCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return memPlayback{m}, nil
}

type memPlayback struct{ m *memSpeaker }

func (p memPlayback) Write(b []byte) (int, error) {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	return p.m.heard.Write(b)
}

func (p memPlayback) Close() error {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	p.m.closed++
	return nil
}

var talkRules = talk.Settings{Enabled: true, IdleTimeout: 3 * time.Second, MaxDuration: time.Minute, HoldOff: 10 * time.Second}

func newTalkHandler(s talk.Settings) (http.Handler, *memSpeaker, *bytes.Buffer) {
	sp := &memSpeaker{}
	logs := &bytes.Buffer{}
	return NewHandler(Deps{
		Logger:  slog.New(slog.NewTextHandler(logs, nil)),
		Cameras: []domain.Camera{dog()},
		Engine:  newEngine(),
		Talk:    talk.New(sp, s, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}), sp, logs
}

func send(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rr
}

func startTalk(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := do(h, http.MethodPost, "/api/cameras/dog/talk")
	if rr.Code != http.StatusCreated {
		t.Fatalf("start: status %d: %s", rr.Code, rr.Body)
	}
	var res struct{ Session string }
	decode(t, rr, &res)
	if len(res.Session) != 32 {
		t.Fatalf("session = %q", res.Session)
	}
	return res.Session
}

func TestTalkPlaysChunksInOrderAndStops(t *testing.T) {
	h, sp, logs := newTalkHandler(talkRules)
	session := startTalk(t, h)
	base := "/api/talk/" + session
	for _, c := range []struct{ seq, body string }{{"0", "he"}, {"2", "o"}, {"1", "ll"}} {
		if rr := send(h, http.MethodPut, base+"/chunks/"+c.seq, c.body); rr.Code != http.StatusNoContent {
			t.Fatalf("chunk %s: status %d: %s", c.seq, rr.Code, rr.Body)
		}
	}
	if strings.Contains(logs.String(), "/chunks/") {
		t.Error("successful chunks must not fill the access log")
	}
	if rr := do(h, http.MethodDelete, base); rr.Code != http.StatusNoContent {
		t.Fatalf("stop: status %d", rr.Code)
	}
	if got := sp.heard.String(); got != "hello" || sp.closed != 1 {
		t.Errorf("heard %q, closed %d times", got, sp.closed)
	}

	// After the stop the session is gone; stopping again is still fine.
	if rr := send(h, http.MethodPut, base+"/chunks/3", "x"); rr.Code != http.StatusGone {
		t.Errorf("chunk after stop: status %d, want 410", rr.Code)
	}
	if !strings.Contains(logs.String(), "/chunks/3") {
		t.Error("a failed chunk should be in the access log")
	}
	if rr := do(h, http.MethodDelete, base); rr.Code != http.StatusNoContent {
		t.Errorf("second stop: status %d", rr.Code)
	}
}

func TestTalkRejectsBadRequests(t *testing.T) {
	h, _, _ := newTalkHandler(talkRules)
	session := startTalk(t, h)
	base := "/api/talk/" + session + "/chunks/"
	for path, want := range map[string]int{
		base + "-1":               http.StatusBadRequest,
		base + "abc":              http.StatusBadRequest,
		base + "99999999":         http.StatusBadRequest,
		"/api/talk/nope/chunks/0": http.StatusGone,
	} {
		if rr := send(h, http.MethodPut, path, "x"); rr.Code != want {
			t.Errorf("PUT %s: status %d, want %d", path, rr.Code, want)
		}
	}
	if rr := send(h, http.MethodPut, base+"0", strings.Repeat("x", maxTalkChunk+1)); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized chunk: status %d, want 413", rr.Code)
	}
	// Far too many chunks ahead of the missing one.
	last := 0
	for seq := 1; seq <= 40 && last != http.StatusConflict; seq++ {
		last = send(h, http.MethodPut, base+itoa(seq), "x").Code
	}
	if last != http.StatusConflict {
		t.Errorf("chunks far ahead: last status %d, want 409", last)
	}
	if rr := do(h, http.MethodPost, "/api/cameras/cat/talk"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown camera: status %d", rr.Code)
	}
}

func itoa(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestTalkStartFailures(t *testing.T) {
	off := talkRules
	off.Enabled = false
	h, _, _ := newTalkHandler(off)
	if rr := do(h, http.MethodPost, "/api/cameras/dog/talk"); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("disabled: status %d, want 503", rr.Code)
	}
	var cam cameraView
	decode(t, do(h, http.MethodGet, "/api/cameras/dog"), &cam)
	if cam.CanTalk {
		t.Error("canTalk = true although talking is off")
	}

	h, sp, _ := newTalkHandler(talkRules)
	decode(t, do(h, http.MethodGet, "/api/cameras/dog"), &cam)
	if !cam.CanTalk {
		t.Error("canTalk = false although talking is on")
	}
	sp.err = errors.New("ffplay not found")
	rr := do(h, http.MethodPost, "/api/cameras/dog/talk")
	if rr.Code != http.StatusBadGateway || strings.Contains(rr.Body.String(), "ffplay") {
		t.Errorf("speaker failure: status %d, body %s (must not leak details)", rr.Code, rr.Body)
	}
}

func TestTalkRoutesAreAbsentWithoutTheService(t *testing.T) {
	h := newTest(t, newEngine(), "")
	if rr := do(h, http.MethodPost, "/api/cameras/dog/talk"); rr.Code == http.StatusCreated {
		t.Errorf("status %d", rr.Code)
	}
	var cam cameraView
	decode(t, do(h, http.MethodGet, "/api/cameras/dog"), &cam)
	if cam.CanTalk {
		t.Error("canTalk without the service")
	}
}
