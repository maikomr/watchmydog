package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// EventID identifies an event. It is random, not sequential, so edge nodes
// can create events offline and a cloud store can merge them later without
// collisions.
type EventID string

// NewEventID returns a fresh random ID (128 bits, hex).
func NewEventID() EventID {
	var b [16]byte
	// crypto/rand.Read never fails on supported platforms (Go 1.24+).
	_, _ = rand.Read(b[:])
	return EventID(hex.EncodeToString(b[:]))
}

// EventKind says what was detected.
type EventKind string

const (
	// EventSound: the camera's microphone was loud for a while (howling,
	// barking, anything above the configured level).
	EventSound EventKind = "sound"
)

// Event is something that happened at a camera and is worth showing later.
type Event struct {
	ID        EventID
	CameraID  CameraID
	Kind      EventKind
	StartedAt time.Time
	// EndedAt is nil while the event is still going on.
	EndedAt *time.Time
	// PeakDBFS is the loudest 100 ms of the event (RMS, dB relative to
	// full scale: 0 is the loudest the microphone can report).
	PeakDBFS float64
}

// Ongoing reports whether the event has not ended yet.
func (e Event) Ongoing() bool { return e.EndedAt == nil }

// SilenceDBFS is the level reported for digital silence. A true zero signal
// is minus infinity in dB, which JSON cannot carry, so levels stop here.
const SilenceDBFS = -100.0

// SoundLevel is one measurement of how loud a camera's microphone is.
type SoundLevel struct {
	CameraID CameraID
	At       time.Time
	// DBFS is the RMS level of the measured window, SilenceDBFS..0.
	DBFS float64
	// Loud is true while a sound event is open for this camera.
	Loud bool
}

// SoundDetection is what a detector reports when a noise starts and ends.
// It becomes an Event once the application gives it an ID and stores it.
type SoundDetection struct {
	CameraID  CameraID
	StartedAt time.Time
	// EndedAt is the zero time while the noise is still going on.
	EndedAt  time.Time
	PeakDBFS float64
}
