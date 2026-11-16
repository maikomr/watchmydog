// Package cameras makes "release the camera" last: it wraps the stream
// engine and writes every pause and resume to a store, and at start it tells
// the engine which cameras were paused before.
//
// Without it the paused state lived only in the engine's memory, and any
// restart of the service (crash, reboot, update) took the camera back from
// the program the owner had released it for.
package cameras

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Engine is a ports.StreamEngine that remembers paused cameras. All other
// methods go straight to the wrapped engine.
type Engine struct {
	ports.StreamEngine
	store  ports.PauseStore
	logger *slog.Logger
}

var _ ports.StreamEngine = (*Engine)(nil)

// Remember wraps engine.
func Remember(engine ports.StreamEngine, store ports.PauseStore, logger *slog.Logger) *Engine {
	return &Engine{StreamEngine: engine, store: store, logger: logger}
}

// Start starts the engine with the stored paused cameras (plus any given in
// paused) already paused.
//
// If the store cannot be read, Start fails instead of starting everything:
// opening a camera the owner released for a call is worse than the service
// starting a little later (launchd retries).
func (e *Engine) Start(ctx context.Context, cameras []domain.Camera, paused []domain.CameraID) error {
	stored, err := e.store.PausedCameras(ctx)
	if err != nil {
		return fmt.Errorf("read paused cameras: %w", err)
	}
	if len(stored) > 0 {
		e.logger.Info("cameras stay released from before the restart", "cameras", stored)
	}
	return e.StreamEngine.Start(ctx, cameras, append(stored, paused...))
}

// Pause records the pause, then pauses.
//
// The record is written first: if the service dies between the two steps,
// the camera is released after the restart, which is what was asked for.
// The other order could bring a released camera back.
func (e *Engine) Pause(ctx context.Context, id domain.CameraID) error {
	if _, err := e.StreamEngine.Status(ctx, id); err != nil {
		return err // unknown camera: nothing to record
	}
	if err := e.store.SetPaused(ctx, id, true); err != nil {
		return fmt.Errorf("remember pause: %w", err)
	}
	return e.StreamEngine.Pause(ctx, id)
}

// Resume resumes, then forgets the pause.
//
// If forgetting fails the camera is live now but would be released again
// after a restart, so the error is returned and logged; resuming again
// repairs it.
func (e *Engine) Resume(ctx context.Context, id domain.CameraID) error {
	if err := e.StreamEngine.Resume(ctx, id); err != nil {
		return err
	}
	if err := e.store.SetPaused(ctx, id, false); err != nil {
		e.logger.Error("camera resumed but the stored pause could not be removed; it will be released again after a restart", "camera", id, "err", err)
		return fmt.Errorf("forget pause: %w", err)
	}
	return nil
}
