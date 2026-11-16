package go2rtc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Engine implements ports.StreamEngine with a supervised go2rtc process.
//
// Pause/Resume work by re-rendering go2rtc's config without/with the camera
// and restarting go2rtc. go2rtc's runtime API can add and remove stream
// entries, but removing one does not stop its capture processes while
// viewers are attached, and we need the camera released deterministically
// (ADR-0002 amendment). A restart takes well under a second and only
// affects viewers of this edge node, who reconnect automatically.
type Engine struct {
	settings   Settings
	configPath string
	logger     *slog.Logger
	client     *client
	proxy      http.Handler
	sup        *supervisor
	// readyTimeout bounds how long Start waits for go2rtc's API.
	readyTimeout time.Duration

	mu      sync.Mutex
	cameras map[domain.CameraID]domain.Camera
	paused  map[domain.CameraID]bool
}

var _ ports.StreamEngine = (*Engine)(nil)

// New prepares an engine; nothing runs until Start.
func New(s Settings, dataDir string, logger *slog.Logger) *Engine {
	return &Engine{
		settings:     s,
		configPath:   filepath.Join(dataDir, "go2rtc.yaml"),
		logger:       logger,
		client:       newClient(s.APIAddr),
		proxy:        newProxy(s.APIAddr),
		readyTimeout: 10 * time.Second,
		cameras:      map[domain.CameraID]domain.Camera{},
		paused:       map[domain.CameraID]bool{},
	}
}

// Start renders the config, launches go2rtc and waits for its API. Cameras
// listed in paused are left out of the first config, so their devices are
// not opened even for a moment.
func (e *Engine) Start(ctx context.Context, cameras []domain.Camera, paused []domain.CameraID) error {
	e.mu.Lock()
	for _, c := range cameras {
		e.cameras[c.ID] = c
	}
	for _, id := range paused {
		if _, ok := e.cameras[id]; ok {
			e.paused[id] = true
		}
	}
	e.mu.Unlock()

	// Resolve executables now so a missing tool fails fast with a clear
	// message instead of a go2rtc restart loop.
	bin, err := exec.LookPath(e.settings.Bin)
	if err != nil {
		return fmt.Errorf("go2rtc binary: %w", err)
	}
	for name, p := range map[string]*string{"ffmpeg": &e.settings.FFmpegBin, "wmd-capture": &e.settings.CaptureBin} {
		resolved, err := exec.LookPath(*p)
		if err != nil {
			return fmt.Errorf("%s binary: %w", name, err)
		}
		*p = resolved
	}

	if err := e.writeConfig(); err != nil {
		return err
	}
	e.sup = newSupervisor(bin, []string{"-config", e.configPath}, e.logger)
	if err := e.sup.Start(); err != nil {
		return err
	}
	return e.waitReady(ctx)
}

// Stop terminates go2rtc, which also ends every capture process.
func (e *Engine) Stop(ctx context.Context) error {
	if e.sup == nil {
		return nil
	}
	return e.sup.Stop(ctx)
}

// Healthy reports whether go2rtc is running and answering.
func (e *Engine) Healthy(ctx context.Context) error {
	if e.sup == nil || !e.sup.Alive() {
		return fmt.Errorf("go2rtc process not running")
	}
	return e.client.ping(ctx)
}

// Status derives the camera's stream state from go2rtc's stream list.
func (e *Engine) Status(ctx context.Context, id domain.CameraID) (domain.StreamStatus, error) {
	e.mu.Lock()
	_, known := e.cameras[id]
	paused := e.paused[id]
	e.mu.Unlock()
	if !known {
		return domain.StreamStatus{}, ports.ErrUnknownCamera{ID: id}
	}
	st := domain.StreamStatus{CameraID: id, State: domain.StreamIdle}
	if paused {
		st.State = domain.StreamPaused
		return st, nil
	}
	streams, err := e.client.streams(ctx)
	if err != nil {
		st.State = domain.StreamError
		st.Detail = err.Error()
		return st, nil
	}
	info, ok := streams[string(id)]
	if !ok {
		st.State = domain.StreamError
		st.Detail = "stream missing in engine"
		return st, nil
	}
	st.Consumers = len(info.Consumers)
	if st.Consumers > 0 {
		st.State = domain.StreamActive
	}
	return st, nil
}

// Pause removes the camera from go2rtc's config and restarts it.
func (e *Engine) Pause(ctx context.Context, id domain.CameraID) error {
	return e.setPaused(ctx, id, true)
}

// Resume puts the camera back and restarts go2rtc.
func (e *Engine) Resume(ctx context.Context, id domain.CameraID) error {
	return e.setPaused(ctx, id, false)
}

func (e *Engine) setPaused(ctx context.Context, id domain.CameraID, paused bool) error {
	e.mu.Lock()
	if _, ok := e.cameras[id]; !ok {
		e.mu.Unlock()
		return ports.ErrUnknownCamera{ID: id}
	}
	if e.paused[id] == paused {
		e.mu.Unlock()
		return nil
	}
	e.paused[id] = paused
	e.mu.Unlock()

	if err := e.writeConfig(); err != nil {
		return err
	}
	if e.sup == nil {
		return nil
	}
	e.logger.Info("restarting engine", "camera", id, "paused", paused)
	if err := e.sup.Restart(ctx); err != nil {
		return err
	}
	return e.waitReady(ctx)
}

// MediaHandler proxies player endpoints to go2rtc.
func (e *Engine) MediaHandler() http.Handler { return e.proxy }

// activeCameras returns the configured cameras that are not paused, sorted.
func (e *Engine) activeCameras() []domain.Camera {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]domain.Camera, 0, len(e.cameras))
	for id, c := range e.cameras {
		if !e.paused[id] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (e *Engine) writeConfig() error {
	data, err := RenderConfig(e.settings, e.activeCameras())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.configPath), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(e.configPath, data, 0o644); err != nil {
		return fmt.Errorf("write go2rtc config: %w", err)
	}
	return nil
}

// waitReady polls go2rtc's API until it answers or the timeout passes.
func (e *Engine) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(e.readyTimeout)
	for {
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		err := e.client.ping(pctx)
		cancel()
		if err == nil {
			return nil
		}
		if !e.sup.Alive() {
			return fmt.Errorf("go2rtc exited during startup (see log)")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("go2rtc not ready after %s: %w", e.readyTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
