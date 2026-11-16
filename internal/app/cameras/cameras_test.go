package cameras

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"testing"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// fakeEngine keeps the paused state in memory, like the real one.
type fakeEngine struct {
	known       map[domain.CameraID]bool
	paused      map[domain.CameraID]bool
	startPaused []domain.CameraID
	started     bool
	failWith    error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{known: map[domain.CameraID]bool{}, paused: map[domain.CameraID]bool{}}
}

func (f *fakeEngine) Start(_ context.Context, cams []domain.Camera, paused []domain.CameraID) error {
	f.started = true
	f.startPaused = paused
	for _, c := range cams {
		f.known[c.ID] = true
	}
	for _, id := range paused {
		f.paused[id] = true
	}
	return nil
}
func (f *fakeEngine) Stop(context.Context) error    { return nil }
func (f *fakeEngine) Healthy(context.Context) error { return nil }
func (f *fakeEngine) Status(_ context.Context, id domain.CameraID) (domain.StreamStatus, error) {
	if !f.known[id] {
		return domain.StreamStatus{}, ports.ErrUnknownCamera{ID: id}
	}
	return domain.StreamStatus{CameraID: id}, nil
}
func (f *fakeEngine) Pause(_ context.Context, id domain.CameraID) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.paused[id] = true
	return nil
}
func (f *fakeEngine) Resume(_ context.Context, id domain.CameraID) error {
	if f.failWith != nil {
		return f.failWith
	}
	delete(f.paused, id)
	return nil
}
func (f *fakeEngine) MediaHandler() http.Handler { return http.NotFoundHandler() }

var (
	ctx    = context.Background()
	quiet  = slog.New(slog.NewTextHandler(io.Discard, nil))
	dogCat = []domain.Camera{{ID: "dog"}, {ID: "cat"}}
)

func stored(t *testing.T, s ports.PauseStore) []domain.CameraID {
	t.Helper()
	got, err := s.PausedCameras(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAReleasedCameraStaysReleasedAfterARestart(t *testing.T) {
	store := memory.New()

	first := newFakeEngine()
	e := Remember(first, store, quiet)
	if err := e.Start(ctx, dogCat, nil); err != nil {
		t.Fatal(err)
	}
	if len(first.startPaused) != 0 {
		t.Fatalf("fresh start paused %v", first.startPaused)
	}
	if err := e.Pause(ctx, "dog"); err != nil {
		t.Fatal(err)
	}
	if !first.paused["dog"] {
		t.Fatal("engine was not paused")
	}

	// The service restarts: a new engine with empty memory, the same store.
	second := newFakeEngine()
	e = Remember(second, store, quiet)
	if err := e.Start(ctx, dogCat, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second.startPaused, []domain.CameraID{"dog"}) {
		t.Fatalf("restart started with paused = %v, want [dog]", second.startPaused)
	}

	// Resuming is remembered too.
	if err := e.Resume(ctx, "dog"); err != nil {
		t.Fatal(err)
	}
	third := newFakeEngine()
	if err := Remember(third, store, quiet).Start(ctx, dogCat, nil); err != nil {
		t.Fatal(err)
	}
	if len(third.startPaused) != 0 {
		t.Fatalf("after resume, restart paused %v", third.startPaused)
	}
}

func TestStartFailsWhenTheStoreCannotBeRead(t *testing.T) {
	store := memory.New()
	store.FailWith = errors.New("disk on fire")
	eng := newFakeEngine()
	if err := Remember(eng, store, quiet).Start(ctx, dogCat, nil); err == nil {
		t.Fatal("started without knowing which cameras are released")
	}
	if eng.started {
		t.Error("the engine was started")
	}
}

func TestPauseIsRecordedBeforeTheEngineActs(t *testing.T) {
	store := memory.New()
	eng := newFakeEngine()
	e := Remember(eng, store, quiet)
	if err := e.Start(ctx, dogCat, nil); err != nil {
		t.Fatal(err)
	}

	// The store fails: the camera keeps running and the caller hears why.
	store.FailWith = errors.New("disk full")
	if err := e.Pause(ctx, "dog"); err == nil {
		t.Fatal("pause succeeded although it could not be remembered")
	}
	if eng.paused["dog"] {
		t.Error("engine paused although the pause was not recorded")
	}
	store.FailWith = nil

	// The engine fails after the record was written: the record stays, so
	// a restart releases the camera as asked.
	eng.failWith = errors.New("go2rtc did not come back")
	if err := e.Pause(ctx, "dog"); err == nil {
		t.Fatal("engine error was swallowed")
	}
	if got := stored(t, store); !reflect.DeepEqual(got, []domain.CameraID{"dog"}) {
		t.Errorf("stored = %v, want [dog]", got)
	}
}

func TestResumeKeepsTheRecordWhenTheEngineFails(t *testing.T) {
	store := memory.New()
	eng := newFakeEngine()
	e := Remember(eng, store, quiet)
	if err := e.Start(ctx, dogCat, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Pause(ctx, "dog"); err != nil {
		t.Fatal(err)
	}

	eng.failWith = errors.New("go2rtc did not come back")
	if err := e.Resume(ctx, "dog"); err == nil {
		t.Fatal("engine error was swallowed")
	}
	if got := stored(t, store); len(got) != 1 {
		t.Errorf("stored = %v, want the pause kept", got)
	}
	eng.failWith = nil

	store.FailWith = errors.New("disk full")
	if err := e.Resume(ctx, "dog"); err == nil {
		t.Fatal("resume reported success although the pause is still stored")
	}
	store.FailWith = nil
	if err := e.Resume(ctx, "dog"); err != nil {
		t.Fatalf("resuming again must repair it: %v", err)
	}
	if got := stored(t, store); len(got) != 0 {
		t.Errorf("stored = %v, want none", got)
	}
}

func TestUnknownCameraIsNotRecorded(t *testing.T) {
	store := memory.New()
	e := Remember(newFakeEngine(), store, quiet)
	if err := e.Start(ctx, dogCat, nil); err != nil {
		t.Fatal(err)
	}
	var unknown ports.ErrUnknownCamera
	if err := e.Pause(ctx, "nope"); !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want ErrUnknownCamera", err)
	}
	if got := stored(t, store); len(got) != 0 {
		t.Errorf("stored = %v", got)
	}
}
