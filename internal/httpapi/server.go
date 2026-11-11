// Package httpapi is the HTTP adapter of watchmydog: it exposes the domain
// over a JSON API, proxies the media engine and serves the web client. It
// holds no business logic; handlers translate HTTP into calls on the ports
// defined in internal/ports.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Deps are the collaborators the HTTP layer needs. Keeping them in a struct
// lets main() wire real adapters and tests wire fakes.
type Deps struct {
	Version string
	Commit  string
	Logger  *slog.Logger
	// Now is injected so handlers that report time are testable.
	Now func() time.Time

	Cameras []domain.Camera
	Engine  ports.StreamEngine
	// WebDir is the built web client directory; empty disables static serving.
	WebDir string
}

// NewHandler builds the root http.Handler with all routes registered.
func NewHandler(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", d.handleHealth)
	mux.HandleFunc("GET /api/cameras", d.handleListCameras)
	mux.HandleFunc("GET /api/cameras/{id}", d.handleGetCamera)
	mux.HandleFunc("POST /api/cameras/{id}/pause", d.handlePause)
	mux.HandleFunc("POST /api/cameras/{id}/resume", d.handleResume)

	if d.Engine != nil {
		// Player endpoints of the engine, e.g. /media/api/webrtc?src=dog.
		mux.Handle("/media/", http.StripPrefix("/media", d.Engine.MediaHandler()))
	}
	if d.WebDir != "" {
		mux.Handle("/", spaHandler(d.WebDir))
	}

	return logRequests(d.Logger, mux)
}

type healthResponse struct {
	Status  string    `json:"status"`
	Version string    `json:"version"`
	Commit  string    `json:"commit"`
	Time    time.Time `json:"time"`
	Engine  string    `json:"engine"`
}

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	res := healthResponse{Status: "ok", Version: d.Version, Commit: d.Commit, Time: d.Now().UTC(), Engine: "ok"}
	if d.Engine == nil {
		res.Engine = "not configured"
	} else if err := d.Engine.Healthy(r.Context()); err != nil {
		res.Status = "degraded"
		res.Engine = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

// cameraView is the API shape of a camera plus its live status.
type cameraView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	FPS       int    `json:"fps"`
	HasAudio  bool   `json:"hasAudio"`
	State     string `json:"state"`
	Consumers int    `json:"consumers"`
	Detail    string `json:"detail,omitempty"`
	// Stream names the engine stream to request under /media/ (best quality).
	Stream string `json:"stream"`
	// Streams lists every quality variant, best first.
	Streams []streamView `json:"streams"`
}

type streamView struct {
	Quality string `json:"quality"`
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

func (d Deps) view(r *http.Request, c domain.Camera) cameraView {
	v := cameraView{
		ID: string(c.ID), Name: c.Name, Width: c.Video.Width, Height: c.Video.Height, FPS: c.Video.FPS,
		HasAudio: c.HasAudio(), State: string(domain.StreamError), Stream: string(c.ID),
	}
	for _, sv := range c.Variants() {
		v.Streams = append(v.Streams, streamView{Quality: string(sv.Quality), Name: sv.Name, Width: sv.Width, Height: sv.Height})
	}
	if d.Engine == nil {
		v.Detail = "engine not configured"
		return v
	}
	st, err := d.Engine.Status(r.Context(), c.ID)
	if err != nil {
		v.Detail = err.Error()
		return v
	}
	v.State, v.Consumers, v.Detail = string(st.State), st.Consumers, st.Detail
	return v
}

func (d Deps) handleListCameras(w http.ResponseWriter, r *http.Request) {
	out := make([]cameraView, 0, len(d.Cameras))
	for _, c := range d.Cameras {
		out = append(out, d.view(r, c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cameras": out})
}

func (d Deps) findCamera(id string) (domain.Camera, bool) {
	for _, c := range d.Cameras {
		if string(c.ID) == id {
			return c, true
		}
	}
	return domain.Camera{}, false
}

func (d Deps) handleGetCamera(w http.ResponseWriter, r *http.Request) {
	c, ok := d.findCamera(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown camera")
		return
	}
	writeJSON(w, http.StatusOK, d.view(r, c))
}

func (d Deps) handlePause(w http.ResponseWriter, r *http.Request) {
	d.setPaused(w, r, true)
}

func (d Deps) handleResume(w http.ResponseWriter, r *http.Request) {
	d.setPaused(w, r, false)
}

func (d Deps) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	c, ok := d.findCamera(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown camera")
		return
	}
	if d.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not configured")
		return
	}
	var err error
	if paused {
		err = d.Engine.Pause(r.Context(), c.ID)
	} else {
		err = d.Engine.Resume(r.Context(), c.ID)
	}
	if err != nil {
		var unknown ports.ErrUnknownCamera
		if errors.As(err, &unknown) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		d.Logger.Error("pause/resume", "camera", c.ID, "paused", paused, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d.view(r, c))
}

// spaHandler serves the built web client; unknown paths without a file
// extension fall back to index.html so client-side routes work.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if _, err := os.Stat(p); err != nil && filepath.Ext(r.URL.Path) == "" {
			r.URL.Path = "/"
		}
		fs.ServeHTTP(w, r)
	})
}

// writeJSON encodes v as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; nothing more to do than log.
		slog.Error("write json response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// logRequests is minimal structured access logging. It will grow a request
// ID and latency histogram when observability becomes an increment.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur", time.Since(start).Round(time.Microsecond),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets httputil.ReverseProxy reach the underlying ResponseWriter for
// WebSocket hijacking and flushing.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
