package ports

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
)

// Recorder keeps the last minutes of every camera (the "ring": old footage
// is thrown away as new footage arrives) and cuts clips out of it. Because
// the ring already holds the past, a clip can begin before the noise that
// caused it.
//
// The current implementation records with ffmpeg to local files. A cloud
// deployment would cut the same way and put the clips in object storage.
type Recorder interface {
	// Record keeps the ring of one camera filled until ctx is done. Like
	// SoundDetector.Watch it keeps trying when the stream is unavailable
	// (camera paused, engine restarting) and only returns when ctx ends.
	Record(ctx context.Context, cam domain.Camera) error
	// Cut stores the part of the ring between from and to as the clip id.
	// If to is not recorded yet it waits for it (a bounded time). The clip
	// may be longer than asked, because the ring is cut at fixed points,
	// and shorter if part of the span was never recorded. The result has
	// the real start, duration and size.
	//
	// It returns ErrNothingRecorded when the ring has nothing in the span.
	Cut(ctx context.Context, id domain.ClipID, cam domain.CameraID, from, to time.Time) (domain.Clip, error)
	// Open returns the clip's video file (MP4). ErrClipNotFound if absent.
	Open(id domain.ClipID) (ClipFile, error)
	// Delete removes the clip's file; a missing file is not an error.
	Delete(id domain.ClipID) error
	// Stored lists the clips that have a file, in no particular order.
	Stored() ([]domain.ClipID, error)
}

// ClipFile is a clip's video, seekable so that players can jump around.
type ClipFile interface {
	io.ReadSeekCloser
}

var (
	// ErrNothingRecorded: the ring holds no footage for the requested span.
	ErrNothingRecorded = errors.New("nothing recorded in that time span")
	// ErrClipNotFound: no clip with that ID.
	ErrClipNotFound = errors.New("clip not found")
)

// ClipStore keeps the list of clips (what, when, how big). The video itself
// is the Recorder's.
type ClipStore interface {
	// SaveClip inserts the clip or replaces the one with the same ID.
	SaveClip(ctx context.Context, c domain.Clip) error
	// GetClip returns one clip, or ErrClipNotFound.
	GetClip(ctx context.Context, id domain.ClipID) (domain.Clip, error)
	// ListClips returns clips, newest first. An empty camera means all
	// cameras; limit 0 means no limit.
	ListClips(ctx context.Context, cam domain.CameraID, limit int) ([]domain.Clip, error)
	// DeleteClip removes the clip; an unknown ID is not an error.
	DeleteClip(ctx context.Context, id domain.ClipID) error
}
