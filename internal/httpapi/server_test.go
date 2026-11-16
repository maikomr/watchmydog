package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// fakeEngine records calls and returns scripted statuses.
type fakeEngine struct {
	healthy  error
	statuses map[domain.CameraID]domain.StreamStatus
	calls    []string
	failWith error
}

func (f *fakeEngine) Start(context.Context, []domain.Camera, []domain.CameraID) error {
	return nil
}
func (f *fakeEngine) Stop(context.Context) error    { return nil }
func (f *fakeEngine) Healthy(context.Context) error { return f.healthy }
func (f *fakeEngine) Status(_ context.Context, id domain.CameraID) (domain.StreamStatus, error) {
	st, ok := f.statuses[id]
	if !ok {
		return domain.StreamStatus{}, ports.ErrUnknownCamera{ID: id}
	}
	return st, nil
}
func (f *fakeEngine) Pause(_ context.Context, id domain.CameraID) error {
	f.calls = append(f.calls, "pause "+string(id))
	if f.failWith != nil {
		return f.failWith
	}
	f.statuses[id] = domain.StreamStatus{CameraID: id, State: domain.StreamPaused}
	return nil
}
func (f *fakeEngine) Resume(_ context.Context, id domain.CameraID) error {
	f.calls = append(f.calls, "resume "+string(id))
	if f.failWith != nil {
		return f.failWith
	}
	f.statuses[id] = domain.StreamStatus{CameraID: id, State: domain.StreamIdle}
	return nil
}
func (f *fakeEngine) MediaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("media:" + r.URL.RequestURI()))
	})
}

func dog() domain.Camera {
	return domain.Camera{
		ID: "dog", Name: "Living room",
		Video: domain.VideoSource{Device: "cam", Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2500, Low: &domain.LowRes{Width: 640, Height: 360}},
		Audio: domain.AudioSource{Device: "mic"},
	}
}

func newTest(t *testing.T, eng *fakeEngine, webDir string) http.Handler {
	t.Helper()
	fixed := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	d := Deps{
		Version: "1.2.3",
		Commit:  "abc1234",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:     func() time.Time { return fixed },
		Cameras: []domain.Camera{dog()},
		WebDir:  webDir,
	}
	if eng != nil {
		d.Engine = eng
	}
	return NewHandler(d)
}

func newEngine() *fakeEngine {
	return &fakeEngine{statuses: map[domain.CameraID]domain.StreamStatus{
		"dog": {CameraID: "dog", State: domain.StreamActive, Consumers: 1},
	}}
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

func decode(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
}

func TestHealthReportsEngine(t *testing.T) {
	h := newTest(t, newEngine(), "")
	rr := do(h, http.MethodGet, "/api/health")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body healthResponse
	decode(t, rr, &body)
	if body.Status != "ok" || body.Engine != "ok" || body.Version != "1.2.3" {
		t.Errorf("body = %+v", body)
	}

	eng := newEngine()
	eng.healthy = errors.New("go2rtc down")
	rr = do(newTest(t, eng, ""), http.MethodGet, "/api/health")
	decode(t, rr, &body)
	if body.Status != "degraded" || body.Engine != "go2rtc down" {
		t.Errorf("degraded body = %+v", body)
	}

	rr = do(newTest(t, nil, ""), http.MethodGet, "/api/health")
	decode(t, rr, &body)
	if body.Engine != "not configured" {
		t.Errorf("no-engine body = %+v", body)
	}
}

func TestListAndGetCameras(t *testing.T) {
	h := newTest(t, newEngine(), "")
	rr := do(h, http.MethodGet, "/api/cameras")
	var list struct{ Cameras []cameraView }
	decode(t, rr, &list)
	if len(list.Cameras) != 1 {
		t.Fatalf("cameras = %+v", list.Cameras)
	}
	c := list.Cameras[0]
	if c.ID != "dog" || c.State != "active" || c.Consumers != 1 || !c.HasAudio || c.Stream != "dog" || c.Width != 1280 {
		t.Errorf("camera view = %+v", c)
	}
	if len(c.Streams) != 2 || c.Streams[0].Quality != "high" || c.Streams[1].Name != "dog_low" || c.Streams[1].Width != 640 {
		t.Errorf("stream variants = %+v", c.Streams)
	}

	rr = do(h, http.MethodGet, "/api/cameras/dog")
	if rr.Code != http.StatusOK {
		t.Errorf("get status = %d", rr.Code)
	}
	rr = do(h, http.MethodGet, "/api/cameras/nope")
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown camera status = %d", rr.Code)
	}
}

func TestPauseResume(t *testing.T) {
	eng := newEngine()
	h := newTest(t, eng, "")
	rr := do(h, http.MethodPost, "/api/cameras/dog/pause")
	if rr.Code != http.StatusOK {
		t.Fatalf("pause status = %d %s", rr.Code, rr.Body.String())
	}
	var v cameraView
	decode(t, rr, &v)
	if v.State != "paused" {
		t.Errorf("after pause view = %+v", v)
	}
	rr = do(h, http.MethodPost, "/api/cameras/dog/resume")
	decode(t, rr, &v)
	if rr.Code != http.StatusOK || v.State != "idle" {
		t.Errorf("after resume = %d %+v", rr.Code, v)
	}
	if strings.Join(eng.calls, ",") != "pause dog,resume dog" {
		t.Errorf("engine calls = %v", eng.calls)
	}

	if rr := do(h, http.MethodPost, "/api/cameras/nope/pause"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown camera pause status = %d", rr.Code)
	}
	if rr := do(h, http.MethodGet, "/api/cameras/dog/pause"); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET pause status = %d", rr.Code)
	}

	eng.failWith = errors.New("restart failed")
	if rr := do(h, http.MethodPost, "/api/cameras/dog/pause"); rr.Code != http.StatusBadGateway {
		t.Errorf("engine failure status = %d", rr.Code)
	}
	if rr := do(newTest(t, nil, ""), http.MethodPost, "/api/cameras/dog/pause"); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("no engine status = %d", rr.Code)
	}
}

func TestMediaIsProxiedWithPrefixStripped(t *testing.T) {
	h := newTest(t, newEngine(), "")
	rr := do(h, http.MethodGet, "/media/api/webrtc?src=dog")
	if rr.Code != http.StatusOK || rr.Body.String() != "media:/api/webrtc?src=dog" {
		t.Errorf("proxied response = %d %q", rr.Code, rr.Body.String())
	}
	if rr := do(newTest(t, nil, ""), http.MethodGet, "/media/api/webrtc"); rr.Code != http.StatusNotFound {
		t.Errorf("without engine /media should 404, got %d", rr.Code)
	}
}

func TestStaticWebClientWithSPAFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o644)
	os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "assets", "a.js"), []byte("js"), 0o644)
	h := newTest(t, newEngine(), dir)

	if rr := do(h, http.MethodGet, "/"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "app") {
		t.Errorf("index = %d %q", rr.Code, rr.Body.String())
	}
	if rr := do(h, http.MethodGet, "/assets/a.js"); rr.Body.String() != "js" {
		t.Errorf("asset = %q", rr.Body.String())
	}
	if rr := do(h, http.MethodGet, "/clips"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "app") {
		t.Errorf("client route should fall back to index, got %d", rr.Code)
	}
	if rr := do(h, http.MethodGet, "/missing.png"); rr.Code != http.StatusNotFound {
		t.Errorf("missing file with extension should 404, got %d", rr.Code)
	}
	// API routes win over the static handler.
	if rr := do(h, http.MethodGet, "/api/health"); rr.Code != http.StatusOK {
		t.Errorf("api under static = %d", rr.Code)
	}
}

func TestUnknownAPIRouteIs404(t *testing.T) {
	if rr := do(newTest(t, newEngine(), ""), http.MethodGet, "/api/nope"); rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestStaticCachingAndPWAFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "sw.js"), []byte("self.addEventListener('fetch',()=>{})"), 0o644)
	os.WriteFile(filepath.Join(dir, "manifest.webmanifest"), []byte(`{"name":"watchmydog"}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "assets", "main-abc123.js"), []byte("js"), 0o644)
	h := newTest(t, newEngine(), dir)

	cases := []struct {
		path, cacheControl, contentType string
	}{
		{"/", "no-cache", "text/html"},
		{"/clips", "no-cache", "text/html"},
		{"/sw.js", "no-cache", "text/javascript"},
		{"/manifest.webmanifest", "no-cache", "application/manifest+json"},
		{"/assets/main-abc123.js", "public, max-age=31536000, immutable", "text/javascript"},
	}
	for _, c := range cases {
		rr := do(h, http.MethodGet, c.path)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status %d", c.path, rr.Code)
			continue
		}
		if got := rr.Header().Get("Cache-Control"); got != c.cacheControl {
			t.Errorf("%s: Cache-Control = %q, want %q", c.path, got, c.cacheControl)
		}
		if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, c.contentType) {
			t.Errorf("%s: Content-Type = %q, want %s", c.path, got, c.contentType)
		}
	}
}
