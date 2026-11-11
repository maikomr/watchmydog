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
	// Low, when set, adds a second, down-scaled stream for slow networks.
	Low *LowRes
}

// LowRes describes the reduced-quality variant of a camera's video. It is
// derived from the main stream by the engine (no second capture).
type LowRes struct {
	Width  int
	Height int
}

// StreamQuality names a stream variant of a camera.
type StreamQuality string

const (
	QualityHigh StreamQuality = "high"
	QualityLow  StreamQuality = "low"
)

// StreamVariant is one playable stream of a camera.
type StreamVariant struct {
	Quality StreamQuality
	// Name is the engine's stream name to request under /media/.
	Name   string
	Width  int
	Height int
}

// LowStreamName is the engine stream name of the low-res variant.
func (c Camera) LowStreamName() string { return string(c.ID) + "_low" }

// Variants lists the playable streams of the camera, best quality first.
func (c Camera) Variants() []StreamVariant {
	v := []StreamVariant{{Quality: QualityHigh, Name: string(c.ID), Width: c.Video.Width, Height: c.Video.Height}}
	if c.Video.Low != nil {
		v = append(v, StreamVariant{Quality: QualityLow, Name: c.LowStreamName(), Width: c.Video.Low.Width, Height: c.Video.Low.Height})
	}
	return v
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
	if l := c.Video.Low; l != nil {
		if l.Width <= 0 || l.Height <= 0 {
			errs = append(errs, fmt.Errorf("camera %s: video.low width/height must be positive", c.ID))
		} else if l.Width >= c.Video.Width {
			errs = append(errs, fmt.Errorf("camera %s: video.low width must be smaller than the main width", c.ID))
		}
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
