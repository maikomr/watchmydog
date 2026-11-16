// Package sqlite is a ports.EventStore, ports.SubscriptionStore and
// ports.PauseStore backed by one SQLite file.
//
// It uses modernc.org/sqlite, a pure-Go build of SQLite: no C compiler is
// needed and the service stays a single binary (ADR-0007).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// migrations are applied in order, once each. The database remembers how
// many have run in SQLite's user_version field. To change the schema, append
// a statement; never edit an existing one.
var migrations = []string{
	// Times are Unix milliseconds in UTC: compact, sortable, no time-zone
	// parsing. ended_at is NULL while the event is going on.
	`CREATE TABLE events (
		id         TEXT PRIMARY KEY,
		camera_id  TEXT NOT NULL,
		kind       TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		ended_at   INTEGER,
		peak_dbfs  REAL NOT NULL
	) STRICT;
	CREATE INDEX events_by_start ON events (started_at);`,
	// Devices that receive push notifications (ADR-0008). The endpoint URL
	// is the identity the browser gives a subscription.
	`CREATE TABLE push_subscriptions (
		endpoint   TEXT PRIMARY KEY,
		p256dh     TEXT NOT NULL,
		auth       TEXT NOT NULL,
		created_at INTEGER NOT NULL
	) STRICT;`,
	// Cameras the owner paused ("released"). A row means paused; resuming
	// deletes it. Kept so a restart does not take the camera back.
	`CREATE TABLE paused_cameras (
		camera_id TEXT PRIMARY KEY,
		paused_at INTEGER NOT NULL
	) STRICT;`,
}

// Store implements ports.EventStore, ports.SubscriptionStore and
// ports.PauseStore.
type Store struct {
	db *sql.DB
}

var (
	_ ports.EventStore        = (*Store)(nil)
	_ ports.SubscriptionStore = (*Store)(nil)
	_ ports.PauseStore        = (*Store)(nil)
)

// Open opens (creating if needed) the database file and brings its schema
// up to date.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database dir: %w", err)
	}
	// WAL: readers do not block the writer. busy_timeout: wait instead of
	// failing when the file is briefly locked. synchronous=NORMAL is the
	// recommended pairing with WAL (safe against crashes of the service).
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection: SQLite allows a single writer anyway, and our load is
	// a few rows per hour.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this build understands (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA does not accept parameters; the value is our own integer.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SaveEvent implements ports.EventStore.
func (s *Store) SaveEvent(ctx context.Context, e domain.Event) error {
	var ended any
	if e.EndedAt != nil {
		ended = e.EndedAt.UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (id, camera_id, kind, started_at, ended_at, peak_dbfs)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			camera_id = excluded.camera_id, kind = excluded.kind,
			started_at = excluded.started_at, ended_at = excluded.ended_at,
			peak_dbfs = excluded.peak_dbfs`,
		string(e.ID), string(e.CameraID), string(e.Kind), e.StartedAt.UnixMilli(), ended, e.PeakDBFS)
	if err != nil {
		return fmt.Errorf("save event: %w", err)
	}
	return nil
}

// ListEvents implements ports.EventStore.
func (s *Store) ListEvents(ctx context.Context, q ports.EventQuery) ([]domain.Event, error) {
	var (
		where []string
		args  []any
	)
	if q.CameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, string(q.CameraID))
	}
	if !q.Since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, q.Since.UnixMilli())
	}
	if !q.Until.IsZero() {
		where = append(where, "started_at < ?")
		args = append(args, q.Until.UnixMilli())
	}
	query := "SELECT id, camera_id, kind, started_at, ended_at, peak_dbfs FROM events"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY started_at DESC, id LIMIT ?"
	limit := q.Limit
	if limit <= 0 {
		limit = ports.DefaultEventLimit
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var (
			e       domain.Event
			id, cam string
			kind    string
			started int64
			ended   sql.NullInt64
		)
		if err := rows.Scan(&id, &cam, &kind, &started, &ended, &e.PeakDBFS); err != nil {
			return nil, fmt.Errorf("list events: %w", err)
		}
		e.ID, e.CameraID, e.Kind = domain.EventID(id), domain.CameraID(cam), domain.EventKind(kind)
		e.StartedAt = time.UnixMilli(started).UTC()
		if ended.Valid {
			end := time.UnixMilli(ended.Int64).UTC()
			e.EndedAt = &end
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CloseOpenEvents implements ports.EventStore.
func (s *Store) CloseOpenEvents(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE events SET ended_at = started_at WHERE ended_at IS NULL")
	if err != nil {
		return 0, fmt.Errorf("close open events: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// SaveSubscription implements ports.SubscriptionStore.
func (s *Store) SaveSubscription(ctx context.Context, sub domain.PushSubscription) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_subscriptions (endpoint, p256dh, auth, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth`,
		sub.Endpoint, sub.P256DH, sub.Auth, sub.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("save subscription: %w", err)
	}
	return nil
}

// DeleteSubscription implements ports.SubscriptionStore.
func (s *Store) DeleteSubscription(ctx context.Context, endpoint string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE endpoint = ?", endpoint); err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return nil
}

// ListSubscriptions implements ports.SubscriptionStore.
func (s *Store) ListSubscriptions(ctx context.Context) ([]domain.PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT endpoint, p256dh, auth, created_at FROM push_subscriptions ORDER BY created_at, endpoint")
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()
	out := []domain.PushSubscription{}
	for rows.Next() {
		var (
			sub     domain.PushSubscription
			created int64
		)
		if err := rows.Scan(&sub.Endpoint, &sub.P256DH, &sub.Auth, &created); err != nil {
			return nil, fmt.Errorf("list subscriptions: %w", err)
		}
		sub.CreatedAt = time.UnixMilli(created).UTC()
		out = append(out, sub)
	}
	return out, rows.Err()
}

// SetPaused implements ports.PauseStore.
func (s *Store) SetPaused(ctx context.Context, id domain.CameraID, paused bool) error {
	var err error
	if paused {
		_, err = s.db.ExecContext(ctx,
			"INSERT INTO paused_cameras (camera_id, paused_at) VALUES (?, ?) ON CONFLICT (camera_id) DO NOTHING",
			string(id), time.Now().UnixMilli())
	} else {
		_, err = s.db.ExecContext(ctx, "DELETE FROM paused_cameras WHERE camera_id = ?", string(id))
	}
	if err != nil {
		return fmt.Errorf("set paused: %w", err)
	}
	return nil
}

// PausedCameras implements ports.PauseStore.
func (s *Store) PausedCameras(ctx context.Context) ([]domain.CameraID, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT camera_id FROM paused_cameras ORDER BY camera_id")
	if err != nil {
		return nil, fmt.Errorf("paused cameras: %w", err)
	}
	defer rows.Close()
	out := []domain.CameraID{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("paused cameras: %w", err)
		}
		out = append(out, domain.CameraID(id))
	}
	return out, rows.Err()
}

// Close implements ports.EventStore.
func (s *Store) Close() error { return s.db.Close() }
