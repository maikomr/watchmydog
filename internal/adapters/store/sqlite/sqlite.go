// Package sqlite is a ports.EventStore backed by one SQLite file.
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
}

// Store implements ports.EventStore.
type Store struct {
	db *sql.DB
}

var _ ports.EventStore = (*Store)(nil)

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

// Close implements ports.EventStore.
func (s *Store) Close() error { return s.db.Close() }
