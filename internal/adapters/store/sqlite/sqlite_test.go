package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/storetest"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ports.EventStore {
		return open(t, filepath.Join(t.TempDir(), "test.db"))
	})
}

func TestEventsSurviveReopenAndMigrationsRunOnce(t *testing.T) {
	ctx := context.Background()
	// A directory with a space and a "?" checks the path is escaped in the DSN.
	path := filepath.Join(t.TempDir(), "my data?", "wmd.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	e := domain.Event{ID: "a", CameraID: "dog", Kind: domain.EventSound, StartedAt: time.Now(), PeakDBFS: -7}
	if err := s.SaveEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = open(t, path) // would fail with "table events already exists" if migrations re-ran
	got, err := s.ListEvents(ctx, ports.EventQuery{})
	if err != nil || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("after reopen: %v, %v", got, err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Errorf("user_version = %d, err %v; want %d", version, err, len(migrations))
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wmd.db")
	s := open(t, path)
	if _, err := s.db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("opening a database from a newer version must fail")
	}
}
