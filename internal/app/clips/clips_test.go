package clips

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

var (
	ctx   = context.Background()
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
	t0    = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	rules = Settings{
		Enabled: true, PreRoll: 15 * time.Second, PostRoll: 15 * time.Second,
		MaxLength: 5 * time.Minute, MaxBytes: 1000, KeepFor: 24 * time.Hour,
	}
)

func sec(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }

// fakeRecorder "cuts" exactly what is asked and keeps files as a set.
type fakeRecorder struct {
	mu        sync.Mutex
	files     map[domain.ClipID]bool
	size      int64
	cutErr    error
	deleteErr error
	recording chan domain.CameraID
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{files: map[domain.ClipID]bool{}, size: 100, recording: make(chan domain.CameraID, 8)}
}

func (f *fakeRecorder) Record(ctx context.Context, cam domain.Camera) error {
	f.recording <- cam.ID
	<-ctx.Done()
	return nil
}

func (f *fakeRecorder) Cut(_ context.Context, id domain.ClipID, cam domain.CameraID, from, to time.Time) (domain.Clip, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cutErr != nil {
		return domain.Clip{}, f.cutErr
	}
	f.files[id] = true
	return domain.Clip{ID: id, CameraID: cam, StartedAt: from, Duration: to.Sub(from), SizeBytes: f.size}, nil
}

func (f *fakeRecorder) Open(id domain.ClipID) (ports.ClipFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.files[id] {
		return nil, ports.ErrClipNotFound
	}
	return nopCloser{strings.NewReader("video of " + string(id))}, nil
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func (f *fakeRecorder) Delete(id domain.ClipID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.files, id)
	return nil
}

func (f *fakeRecorder) Stored() ([]domain.ClipID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.ClipID{}
	for id := range f.files {
		out = append(out, id)
	}
	return out, nil
}

func (f *fakeRecorder) has(id domain.ClipID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[id]
}

type fixture struct {
	*Service
	rec   *fakeRecorder
	store *memory.Store
	clock time.Time
}

func newFixture(s Settings) *fixture {
	f := &fixture{rec: newFakeRecorder(), store: memory.New(), clock: t0}
	f.Service = New(f.rec, f.store, s, quiet)
	f.now = func() time.Time { return f.clock }
	n := 0
	f.newID = func() domain.ClipID {
		n++
		return domain.ClipID("clip-" + string(rune('0'+n)))
	}
	return f
}

func started(cam domain.CameraID, at time.Time) domain.Event {
	return domain.Event{ID: domain.NewEventID(), CameraID: cam, Kind: domain.EventSound, StartedAt: at}
}

func ended(cam domain.CameraID, start, end time.Time) domain.Event {
	e := started(cam, start)
	e.EndedAt = &end
	return e
}

// spans formats jobs as "cam from..to" in seconds after t0.
func spans(jobs []job) []string {
	out := []string{}
	for _, j := range jobs {
		out = append(out, string(j.cam)+" "+j.from.Sub(t0).String()+".."+j.to.Sub(t0).String())
	}
	return out
}

func wantJobs(t *testing.T, got []job, want ...string) {
	t.Helper()
	g := spans(got)
	if len(g) != len(want) {
		t.Fatalf("jobs = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("jobs = %v, want %v", g, want)
		}
	}
}

func TestAClipCoversTheNoiseWithTimeBeforeAndAfter(t *testing.T) {
	f := newFixture(rules)
	f.EventStarted(started("dog", sec(100)))
	wantJobs(t, f.due(sec(103)))
	f.EventEnded(ended("dog", sec(100), sec(110)))
	wantJobs(t, f.due(sec(124))) // post-roll not over yet
	wantJobs(t, f.due(sec(125)), "dog 1m25s..2m5s")
	wantJobs(t, f.due(sec(200))) // cut once only
}

func TestNoisesCloseTogetherShareOneClip(t *testing.T) {
	f := newFixture(rules)
	f.EventStarted(started("dog", sec(100)))
	f.EventEnded(ended("dog", sec(100), sec(105)))
	// Second bark 10 s later, inside the post-roll of the first.
	f.EventStarted(started("dog", sec(115)))
	wantJobs(t, f.due(sec(121))) // would have been cut at 120 without the second bark
	f.EventEnded(ended("dog", sec(115), sec(118)))
	wantJobs(t, f.due(sec(133)), "dog 1m25s..2m13s")

	// A bark after the cut starts a new clip.
	f.EventStarted(started("dog", sec(300)))
	f.EventEnded(ended("dog", sec(300), sec(301)))
	wantJobs(t, f.due(sec(400)), "dog 4m45s..5m16s")
}

func TestCamerasAreIndependent(t *testing.T) {
	f := newFixture(rules)
	f.EventStarted(started("dog", sec(100)))
	f.EventStarted(started("cat", sec(102)))
	f.EventEnded(ended("cat", sec(102), sec(104)))
	wantJobs(t, f.due(sec(119)), "cat 1m27s..1m59s")
	f.EventEnded(ended("dog", sec(100), sec(130)))
	wantJobs(t, f.due(sec(145)), "dog 1m25s..2m25s")
}

func TestALongNoiseIsSplitAtTheMaximumLength(t *testing.T) {
	s := rules
	s.MaxLength = time.Minute
	f := newFixture(s)
	f.EventStarted(started("dog", sec(100))) // clip begins at 85
	wantJobs(t, f.due(sec(144)))
	wantJobs(t, f.due(sec(145)), "dog 1m25s..2m25s")
	// Still howling: the next clip continues without a gap.
	wantJobs(t, f.due(sec(205)), "dog 2m25s..3m25s")
	f.EventEnded(ended("dog", sec(100), sec(210)))
	wantJobs(t, f.due(sec(225)), "dog 3m25s..3m45s")
	wantJobs(t, f.due(sec(999)))
}

func TestPostRollIsCutShortAtTheMaximumLength(t *testing.T) {
	s := rules
	s.MaxLength = time.Minute
	f := newFixture(s)
	f.EventStarted(started("dog", sec(100)))
	f.EventEnded(ended("dog", sec(100), sec(140))) // would end at 155, limit is 145
	wantJobs(t, f.due(sec(145)), "dog 1m25s..2m25s")
	wantJobs(t, f.due(sec(999)))
	// An end that arrives after the clip was cut is ignored.
	f.EventEnded(ended("dog", sec(100), sec(140)))
	wantJobs(t, f.due(sec(999)))
}

func TestNothingIsCollectedWhenRecordingIsOff(t *testing.T) {
	s := rules
	s.Enabled = false
	f := newFixture(s)
	f.EventStarted(started("dog", sec(100)))
	f.EventEnded(ended("dog", sec(100), sec(110)))
	wantJobs(t, f.due(sec(999)))
}

func TestCutSavesTheClip(t *testing.T) {
	f := newFixture(rules)
	f.cut(ctx, job{cam: "dog", from: sec(85), to: sec(125)})
	clips, used, err := f.List(ctx, "dog", 10)
	if err != nil || len(clips) != 1 || used != 100 {
		t.Fatalf("clips = %+v, used %d, err %v", clips, used, err)
	}
	c := clips[0]
	if c.CameraID != "dog" || !c.StartedAt.Equal(sec(85)) || c.Duration != 40*time.Second || !f.rec.has(c.ID) {
		t.Errorf("clip = %+v", c)
	}
}

func TestCutWithoutFootageOrWithErrorsLeavesNothingBehind(t *testing.T) {
	f := newFixture(rules)
	f.rec.cutErr = ports.ErrNothingRecorded
	f.cut(ctx, job{cam: "dog", from: sec(0), to: sec(10)})
	f.rec.cutErr = errors.New("ffmpeg exploded")
	f.cut(ctx, job{cam: "dog", from: sec(0), to: sec(10)})
	f.rec.cutErr = nil

	// The list cannot be written: the file must not stay.
	f.store.FailWith = errors.New("disk full")
	f.cut(ctx, job{cam: "dog", from: sec(0), to: sec(10)})
	f.store.FailWith = nil

	clips, _, _ := f.List(ctx, "", 0)
	files, _ := f.rec.Stored()
	if len(clips) != 0 || len(files) != 0 {
		t.Errorf("clips = %v, files = %v, want none", clips, files)
	}
}

func save(t *testing.T, f *fixture, id domain.ClipID, start time.Time, size int64) {
	t.Helper()
	f.rec.files[id] = true
	c := domain.Clip{ID: id, CameraID: "dog", StartedAt: start, Duration: time.Minute, SizeBytes: size}
	if err := f.store.SaveClip(ctx, c); err != nil {
		t.Fatal(err)
	}
}

func listed(t *testing.T, f *fixture) string {
	t.Helper()
	clips, _, err := f.List(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, c := range clips {
		ids = append(ids, string(c.ID))
	}
	return strings.Join(ids, " ")
}

func TestSweepDeletesOldestClipsOverTheDiskLimit(t *testing.T) {
	f := newFixture(rules) // limit 1000 bytes
	save(t, f, "a", sec(0), 400)
	save(t, f, "b", sec(100), 400)
	save(t, f, "c", sec(200), 400)
	save(t, f, "d", sec(300), 100)
	f.clock = sec(400)
	f.sweep(ctx) // 1300 bytes: a goes, 900 fit
	if got := listed(t, f); got != "d c b" {
		t.Errorf("clips = %q, want %q", got, "d c b")
	}
	if f.rec.has("a") {
		t.Error("file of the deleted clip is still there")
	}
}

func TestSweepDeletesClipsOlderThanKeepFor(t *testing.T) {
	f := newFixture(rules) // keep 24 h
	save(t, f, "old", sec(0), 10)
	save(t, f, "new", sec(0).Add(2*time.Hour), 10)
	f.clock = sec(0).Add(24*time.Hour + 2*time.Minute)
	f.sweep(ctx)
	if got := listed(t, f); got != "new" {
		t.Errorf("clips = %q, want %q", got, "new")
	}

	// 0 keeps clips whatever their age.
	s := rules
	s.KeepFor = 0
	f = newFixture(s)
	save(t, f, "old", sec(0), 10)
	f.clock = sec(0).Add(1000 * time.Hour)
	f.sweep(ctx)
	if got := listed(t, f); got != "old" {
		t.Errorf("clips = %q, want %q", got, "old")
	}
}

func TestSweepKeepsAClipWhoseFileCannotBeDeleted(t *testing.T) {
	f := newFixture(rules)
	save(t, f, "a", sec(0), 2000)
	f.rec.deleteErr = errors.New("permission denied")
	f.sweep(ctx)
	if got := listed(t, f); got != "a" {
		t.Errorf("clips = %q; the clip must stay listed so it can be deleted later", got)
	}
}

func TestANewClipMakesRoomForItself(t *testing.T) {
	f := newFixture(rules)
	save(t, f, "a", sec(0), 950)
	f.clock = sec(500)
	f.cut(ctx, job{cam: "dog", from: sec(400), to: sec(440)}) // 100 bytes more
	if got := listed(t, f); got != "clip-1" {
		t.Errorf("clips = %q, want only the new one", got)
	}
}

func TestReconcileMakesListAndFilesAgree(t *testing.T) {
	f := newFixture(rules)
	save(t, f, "both", sec(0), 10)
	save(t, f, "listed-only", sec(10), 10)
	delete(f.rec.files, "listed-only")
	f.rec.files["file-only"] = true
	f.reconcile(ctx)
	if got := listed(t, f); got != "both" {
		t.Errorf("clips = %q, want %q", got, "both")
	}
	if f.rec.has("file-only") || !f.rec.has("both") {
		t.Errorf("files = %v", f.rec.files)
	}
}

func TestListFiltersAndCountsAllDiskSpace(t *testing.T) {
	f := newFixture(rules)
	save(t, f, "a", sec(0), 10)
	save(t, f, "b", sec(100), 20)
	cat := domain.Clip{ID: "c", CameraID: "cat", StartedAt: sec(200), SizeBytes: 30}
	if err := f.store.SaveClip(ctx, cat); err != nil {
		t.Fatal(err)
	}
	clips, used, err := f.List(ctx, "dog", 1)
	if err != nil || len(clips) != 1 || clips[0].ID != "b" {
		t.Fatalf("clips = %+v, err %v", clips, err)
	}
	if used != 60 {
		t.Errorf("used = %d, want 60 (all cameras, not only the listed ones)", used)
	}
	if clips, _, _ := f.List(ctx, "bird", 0); clips == nil || len(clips) != 0 {
		t.Errorf("unknown camera: %#v, want empty non-nil", clips)
	}
}

func TestOpenAndDelete(t *testing.T) {
	f := newFixture(rules)
	save(t, f, "a", sec(0), 10)
	clip, file, err := f.Open(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(file)
	file.Close()
	if clip.ID != "a" || string(data) != "video of a" {
		t.Errorf("clip %+v, data %q", clip, data)
	}
	if _, _, err := f.Open(ctx, "nope"); !errors.Is(err, ports.ErrClipNotFound) {
		t.Errorf("Open unknown: %v", err)
	}

	if err := f.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if f.rec.has("a") || listed(t, f) != "" {
		t.Error("clip not fully deleted")
	}
	if err := f.Delete(ctx, "a"); !errors.Is(err, ports.ErrClipNotFound) {
		t.Errorf("Delete again: %v", err)
	}
}

func TestRunRecordsEveryCameraAndCutsDueClips(t *testing.T) {
	f := newFixture(rules)
	var mu sync.Mutex
	clock := t0
	f.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	save(t, f, "orphan-row", sec(0), 10)
	delete(f.rec.files, "orphan-row")

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(runCtx, []domain.Camera{{ID: "dog"}, {ID: "cat"}})
	}()
	seen := map[domain.CameraID]bool{<-f.rec.recording: true, <-f.rec.recording: true}
	if !seen["dog"] || !seen["cat"] {
		t.Errorf("recording %v", seen)
	}

	f.EventStarted(started("dog", sec(100)))
	f.EventEnded(ended("dog", sec(100), sec(110)))
	mu.Lock()
	clock = sec(130)
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for listed(t, f) != "clip-1" {
		if time.Now().After(deadline) {
			t.Fatalf("no clip was cut; clips = %q", listed(t, f))
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRunWithRecordingOffOnlyCleansUp(t *testing.T) {
	s := rules
	s.Enabled = false
	f := newFixture(s)
	save(t, f, "a", sec(0), 5000) // over the limit
	done := make(chan struct{})
	go func() { defer close(done); f.Run(ctx, []domain.Camera{{ID: "dog"}}) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run must return at once when recording is off")
	}
	if listed(t, f) != "" || len(f.rec.recording) != 0 {
		t.Error("expected the clean-up to run and no recording")
	}
}

func TestRingLengthCoversTheLongestClip(t *testing.T) {
	if got := rules.RingLength(); got != 7*time.Minute+15*time.Second {
		t.Errorf("RingLength = %s", got)
	}
}
