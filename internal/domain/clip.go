package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// ClipID identifies a clip. Random for the same reason as EventID. It is
// used in URLs and file names, so its shape is fixed (32 hex digits).
type ClipID string

// NewClipID returns a fresh random ID (128 bits, hex).
func NewClipID() ClipID {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails on supported platforms (Go 1.24+)
	return ClipID(hex.EncodeToString(b[:]))
}

var clipIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Validate reports whether the ID has the shape NewClipID produces.
func (id ClipID) Validate() error {
	if !clipIDRe.MatchString(string(id)) {
		return fmt.Errorf("clip id %q: must be 32 hex digits", id)
	}
	return nil
}

// Clip is a stored piece of video and audio around one or more events: it
// begins a little before the first noise and ends a little after the last.
type Clip struct {
	ID       ClipID
	CameraID CameraID
	// StartedAt is the time of the first picture in the clip.
	StartedAt time.Time
	Duration  time.Duration
	SizeBytes int64
}

// EndedAt is the time of the last picture in the clip.
func (c Clip) EndedAt() time.Time { return c.StartedAt.Add(c.Duration) }
