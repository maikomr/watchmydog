package ports

import (
	"context"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
)

// EventQuery selects events. Zero values mean "no restriction".
type EventQuery struct {
	CameraID domain.CameraID
	// Since and Until bound StartedAt: Since <= StartedAt < Until.
	Since time.Time
	Until time.Time
	// Limit caps the number of results; 0 means the store's default.
	Limit int
}

// EventStore keeps the history of events.
//
// The current implementation is a local SQLite file. A cloud deployment
// would use Postgres behind the same interface.
type EventStore interface {
	// SaveEvent inserts the event, or replaces the one with the same ID
	// (used to set the end of an event that was stored when it started).
	SaveEvent(ctx context.Context, e domain.Event) error
	// ListEvents returns matching events, newest first.
	ListEvents(ctx context.Context, q EventQuery) ([]domain.Event, error)
	// CloseOpenEvents ends every event that has no end, using its start as
	// the end. Called at startup: an event still open then was cut short by
	// a crash or power loss and its real end is unknown. Returns how many.
	CloseOpenEvents(ctx context.Context) (int, error)
	// Close releases the store.
	Close() error
}

// DefaultEventLimit is used by stores when EventQuery.Limit is 0.
const DefaultEventLimit = 100
