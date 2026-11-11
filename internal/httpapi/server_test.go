package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestHandler() http.Handler {
	fixed := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	return NewHandler(Deps{
		Version: "1.2.3",
		Commit:  "abc1234",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:     func() time.Time { return fixed },
	})
}

func TestHealthReturnsOKWithBuildInfo(t *testing.T) {
	h := newTestHandler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	var body healthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" || body.Version != "1.2.3" || body.Commit != "abc1234" {
		t.Errorf("body = %+v", body)
	}
	if !body.Time.Equal(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("time = %v, want injected clock", body.Time)
	}
}

func TestHealthRejectsNonGet(t *testing.T) {
	h := newTestHandler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/health", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	h := newTestHandler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
