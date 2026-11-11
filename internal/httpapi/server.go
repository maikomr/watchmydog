// Package httpapi is the HTTP adapter of watchmydog: it exposes the domain
// over a JSON API and serves the web client. It holds no business logic;
// handlers translate HTTP into calls on the ports defined in internal/ports.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Deps are the collaborators the HTTP layer needs. Keeping them in a struct
// lets main() wire real adapters and tests wire fakes.
type Deps struct {
	Version string
	Commit  string
	Logger  *slog.Logger
	// Now is injected so handlers that report time are testable.
	Now func() time.Time
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

	return logRequests(d.Logger, mux)
}

type healthResponse struct {
	Status  string    `json:"status"`
	Version string    `json:"version"`
	Commit  string    `json:"commit"`
	Time    time.Time `json:"time"`
}

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Version: d.Version,
		Commit:  d.Commit,
		Time:    d.Now().UTC(),
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
