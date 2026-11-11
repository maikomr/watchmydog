package go2rtc

import (
	"context"
	"encoding/json"
	"net"
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

// fakeGo2rtc is an HTTP server that mimics the parts of go2rtc's API the
// engine uses, serving whatever streams the test sets.
type fakeGo2rtc struct {
	srv     *httptest.Server
	streams map[string]any
	hits    []string
}

func newFakeGo2rtc(t *testing.T) *fakeGo2rtc {
	f := &fakeGo2rtc{streams: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/streams", func(w http.ResponseWriter, r *http.Request) {
		f.hits = append(f.hits, r.Method+" "+r.URL.RequestURI())
		_ = json.NewEncoder(w).Encode(f.streams)
	})
	mux.HandleFunc("/api/webrtc", func(w http.ResponseWriter, r *http.Request) {
		f.hits = append(f.hits, r.Method+" "+r.URL.RequestURI()+" host="+r.Host+" origin="+r.Header.Get("Origin"))
		w.Write([]byte("sdp-answer"))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGo2rtc) addr() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func TestEngineStatusFromStreams(t *testing.T) {
	fake := newFakeGo2rtc(t)
	fake.streams["dog"] = map[string]any{
		"producers": []any{map[string]any{"url": "exec:..."}},
		"consumers": []any{map[string]any{"type": "WebRTC"}, map[string]any{"type": "MP4"}},
	}
	e := New(Settings{APIAddr: fake.addr()}, t.TempDir(), quietLogger())
	e.cameras["dog"] = dogCamera()
	e.cameras["cat"] = domain.Camera{ID: "cat"}
	e.sup = newSupervisor("/bin/sh", []string{"-c", "sleep 60"}, quietLogger())
	_ = e.sup.Start()
	t.Cleanup(func() { _ = e.sup.Stop(context.Background()) })

	st, err := e.Status(context.Background(), "dog")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != domain.StreamActive || st.Consumers != 2 {
		t.Errorf("status = %+v", st)
	}

	st, _ = e.Status(context.Background(), "cat")
	if st.State != domain.StreamError || st.Detail == "" {
		t.Errorf("missing stream should be an error state, got %+v", st)
	}

	if _, err := e.Status(context.Background(), "nope"); err == nil {
		t.Error("unknown camera must error")
	} else if _, ok := err.(ports.ErrUnknownCamera); !ok {
		t.Errorf("want ErrUnknownCamera, got %T", err)
	}

	if err := e.Healthy(context.Background()); err != nil {
		t.Errorf("healthy: %v", err)
	}
}

func TestEngineStatusWhenAPIDown(t *testing.T) {
	// Port with nothing listening.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	e := New(Settings{APIAddr: addr}, t.TempDir(), quietLogger())
	e.cameras["dog"] = dogCamera()
	st, err := e.Status(context.Background(), "dog")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != domain.StreamError {
		t.Errorf("expected error state, got %+v", st)
	}
	if err := e.Healthy(context.Background()); err == nil {
		t.Error("expected unhealthy without a process")
	}
}

func TestEnginePauseResumeRewritesConfigAndRestarts(t *testing.T) {
	fake := newFakeGo2rtc(t)
	dir := t.TempDir()
	e := New(Settings{APIAddr: fake.addr(), FFmpegBin: "ffmpeg", CaptureBin: "cap"}, dir, quietLogger())
	e.cameras["dog"] = dogCamera()
	e.sup = newSupervisor("/bin/sh", []string{"-c", "trap 'exit 0' TERM; while true; do sleep 0.1; done"}, quietLogger())
	if err := e.sup.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.sup.Stop(context.Background()) })
	pid1 := e.sup.cmd.Process.Pid

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Pause(ctx, "dog"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "go2rtc.yaml"))
	if strings.Contains(string(cfg), "dog:") {
		t.Errorf("paused camera must not be in config:\n%s", cfg)
	}
	if e.sup.cmd.Process.Pid == pid1 {
		t.Error("expected go2rtc restarted on pause")
	}
	st, _ := e.Status(ctx, "dog")
	if st.State != domain.StreamPaused {
		t.Errorf("expected paused state, got %+v", st)
	}
	// Pausing again is a no-op (no restart).
	pid2 := e.sup.cmd.Process.Pid
	if err := e.Pause(ctx, "dog"); err != nil {
		t.Fatal(err)
	}
	if e.sup.cmd.Process.Pid != pid2 {
		t.Error("pause of a paused camera must not restart")
	}

	if err := e.Resume(ctx, "dog"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(filepath.Join(dir, "go2rtc.yaml"))
	if !strings.Contains(string(cfg), "dog:") {
		t.Errorf("resumed camera must be back in config:\n%s", cfg)
	}
	if err := e.Pause(ctx, "nope"); err == nil {
		t.Error("unknown camera must error")
	}
}

func TestEngineStartFailsFastOnMissingBinaries(t *testing.T) {
	e := New(Settings{Bin: "/nonexistent/go2rtc", FFmpegBin: "ffmpeg", CaptureBin: "cap"}, t.TempDir(), quietLogger())
	err := e.Start(context.Background(), []domain.Camera{dogCamera()})
	if err == nil || !strings.Contains(err.Error(), "go2rtc binary") {
		t.Errorf("expected go2rtc binary error, got %v", err)
	}
}

func TestEngineStartWaitsForAPI(t *testing.T) {
	// A "go2rtc" that serves nothing: Start must time out, not hang.
	fakeBin := fakeBinary(t, `trap 'exit 0' TERM; while true; do sleep 0.1; done`)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	e := New(Settings{Bin: fakeBin, APIAddr: addr, FFmpegBin: "/bin/sh", CaptureBin: "/bin/sh"}, t.TempDir(), quietLogger())
	e.readyTimeout = 300 * time.Millisecond
	err := e.Start(context.Background(), nil)
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected readiness timeout, got %v", err)
	}
}

func TestMediaProxyRewritesHost(t *testing.T) {
	fake := newFakeGo2rtc(t)
	e := New(Settings{APIAddr: fake.addr()}, t.TempDir(), quietLogger())
	// Mount like the HTTP layer does: strip the /media prefix.
	h := http.StripPrefix("/media", e.MediaHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://watchmydog.example/media/api/webrtc?src=dog", strings.NewReader("sdp-offer"))
	req.Header.Set("Origin", "http://watchmydog.example")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "sdp-answer" {
		t.Fatalf("proxy response = %d %q", rr.Code, rr.Body.String())
	}
	last := fake.hits[len(fake.hits)-1]
	want := "POST /api/webrtc?src=dog host=" + fake.addr() + " origin=http://" + fake.addr()
	if last != want {
		t.Errorf("upstream saw %q, want %q", last, want)
	}
}
