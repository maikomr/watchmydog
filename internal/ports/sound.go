package ports

import (
	"context"

	"github.com/maikomr/watchmydog/internal/domain"
)

// SoundDetector listens to a camera's microphone and reports how loud it is
// and when a noise worth recording starts and ends.
//
// The current implementation compares the level with a threshold. A sound
// classifier (is this a howl or a door?) would implement the same interface.
type SoundDetector interface {
	// Watch listens until ctx is done and reports to sink. It keeps trying
	// when the audio is unavailable (camera paused, engine restarting), so it
	// only returns when ctx ends.
	Watch(ctx context.Context, cam domain.Camera, sink SoundSink) error
}

// SoundSink receives what a SoundDetector hears. Calls for one camera arrive
// from a single goroutine, in order; implementations must not block for long.
type SoundSink interface {
	// SoundLevel is called for every measured window (about 10 per second).
	SoundLevel(domain.SoundLevel)
	// SoundStarted is called once when a noise has lasted long enough.
	SoundStarted(domain.SoundDetection)
	// SoundEnded is called once per SoundStarted, with EndedAt set.
	SoundEnded(domain.SoundDetection)
}
