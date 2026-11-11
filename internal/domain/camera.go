// Package domain holds the entities of watchmydog. It has no dependencies on
// adapters, HTTP or external tools, so it can be reused unchanged when parts
// of the system move to a cloud control plane.
package domain

import (
	"errors"
	"fmt"
	"regexp"
)

// CameraID identifies a camera within a site. It is URL-safe by construction.
type CameraID string

var cameraIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Validate reports whether the ID is usable in URLs and config files.
func (id CameraID) Validate() error {
	if !cameraIDRe.MatchString(string(id)) {
		return fmt.Errorf("camera id %q: must match %s", id, cameraIDRe)
	}
	return nil
}

// VideoSource describes how the video of a camera is captured.
type VideoSource struct {
	// Device is the capture device name as reported by `wmd-capture list`.
	Device string
	Width  int
	Height int
	FPS    int
	// BitrateKbps is the target H.264 bitrate.
	BitrateKbps int
}

// AudioSource describes how the audio of a camera is captured.
type AudioSource struct {
	// Device is the audio input name as reported by ffmpeg/avfoundation.
	// Empty means the camera has no audio.
	Device string
}

// Camera is a physical camera plus how it is captured.
type Camera struct {
	ID    CameraID
	Name  string
	Video VideoSource
	Audio AudioSource
}

// HasAudio reports whether an audio track is configured.
func (c Camera) HasAudio() bool { return c.Audio.Device != "" }

// Validate checks the camera definition is complete.
func (c Camera) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	var errs []error
	if c.Video.Device == "" {
		errs = append(errs, fmt.Errorf("camera %s: video.device is required", c.ID))
	}
	if c.Video.Width <= 0 || c.Video.Height <= 0 {
		errs = append(errs, fmt.Errorf("camera %s: video width/height must be positive", c.ID))
	}
	if c.Video.FPS <= 0 {
		errs = append(errs, fmt.Errorf("camera %s: video fps must be positive", c.ID))
	}
	if c.Video.BitrateKbps <= 0 {
		errs = append(errs, fmt.Errorf("camera %s: video bitrate_kbps must be positive", c.ID))
	}
	return errors.Join(errs...)
}

// StreamState is the lifecycle state of a camera's stream in the engine.
type StreamState string

const (
	// StreamIdle: configured, no viewer, capture processes not running.
	StreamIdle StreamState = "idle"
	// StreamActive: at least one consumer, capture running.
	StreamActive StreamState = "active"
	// StreamPaused: removed from the engine on request; camera released.
	StreamPaused StreamState = "paused"
	// StreamError: the engine reports a failing producer.
	StreamError StreamState = "error"
)

// StreamStatus is a point-in-time view of a camera's stream.
type StreamStatus struct {
	CameraID  CameraID
	State     StreamState
	Consumers int
	// Detail carries the engine's error text when State is StreamError.
	Detail string
}
