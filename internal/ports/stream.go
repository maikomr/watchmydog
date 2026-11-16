// Package ports defines the interfaces the application core needs from the
// outside world. Adapters in internal/adapters implement them; the HTTP layer
// and future services depend only on these.
package ports

import (
	"context"
	"net/http"

	"github.com/maikomr/watchmydog/internal/domain"
)

// StreamEngine turns configured cameras into live streams viewers can play.
//
// The current implementation supervises a local go2rtc process. A cloud
// deployment would implement the same interface over a remote SFU.
type StreamEngine interface {
	// Start launches the engine with the given cameras and blocks until it is
	// accepting requests or ctx is done. The cameras listed in paused start
	// paused: their capture devices are never opened. IDs in paused that are
	// not in cameras are ignored.
	Start(ctx context.Context, cameras []domain.Camera, paused []domain.CameraID) error
	// Stop shuts the engine down and releases all capture devices.
	Stop(ctx context.Context) error
	// Healthy returns nil when the engine answers requests.
	Healthy(ctx context.Context) error
	// Status reports the stream state of one camera.
	Status(ctx context.Context, id domain.CameraID) (domain.StreamStatus, error)
	// Pause removes the camera's stream so its capture devices are released
	// (for other apps such as OBS). Viewers are disconnected.
	Pause(ctx context.Context, id domain.CameraID) error
	// Resume re-adds a paused camera's stream.
	Resume(ctx context.Context, id domain.CameraID) error
	// MediaHandler serves the engine's player endpoints (WebRTC signaling,
	// MSE, MP4) to be mounted under a path prefix by the HTTP layer. The
	// handler receives paths relative to the engine root (prefix stripped).
	MediaHandler() http.Handler
}

// PauseStore remembers which cameras the owner paused, so that a restart of
// the service does not take a released camera back.
type PauseStore interface {
	// SetPaused records that the camera is paused (true) or not (false).
	// Repeating a call is not an error.
	SetPaused(ctx context.Context, id domain.CameraID, paused bool) error
	// PausedCameras returns the paused cameras, sorted by ID.
	PausedCameras(ctx context.Context) ([]domain.CameraID, error)
}

// ErrUnknownCamera is returned for IDs that are not configured.
type ErrUnknownCamera struct{ ID domain.CameraID }

func (e ErrUnknownCamera) Error() string { return "unknown camera " + string(e.ID) }
