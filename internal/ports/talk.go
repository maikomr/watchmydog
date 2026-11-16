package ports

import (
	"context"
	"io"

	"github.com/maikomr/watchmydog/internal/domain"
)

// Speaker plays the owner's voice at a camera, so they can talk to whoever
// is in front of it.
//
// The current implementation plays on the Mac's own audio output with
// ffplay. A camera with a built-in speaker would implement the same
// interface over its two-way audio channel.
type Speaker interface {
	// Play starts playing a live audio stream at the camera. The caller
	// writes the stream as the browser recorded it (WebM container, Opus
	// codec) in the order it was recorded; Close ends playback after what
	// was written has been played. ctx cancels playback at once.
	Play(ctx context.Context, cam domain.CameraID) (io.WriteCloser, error)
}
