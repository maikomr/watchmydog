package ffmpeg

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

var (
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
	t0    = time.Date(2026, 11, 16, 19, 30, 40, 0, time.UTC)
	dog   = domain.Camera{ID: "dog"}
)

const clipA = domain.ClipID("0123456789abcdef0123456789abcdef")

func newRecorder(t *testing.T) *Recorder {
	t.Helper()
	r, err := New(Settings{FFmpegBin: "ffmpeg", Dir: t.TempDir(), RingLength: time.Minute, SegmentLength: 10 * time.Second}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	r.poll = 10 * time.Millisecond
	return r
}

// touch creates an empty ring file that begins at start and was last
// written at end.
func touch(t *testing.T, r *Recorder, start, end time.Time) string {
	t.Helper()
	dir := r.ringDir(dog.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, start.UTC().Format(nameLayout)+segmentExt)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, end, end); err != nil {
		t.Fatal(err)
	}
	return p
}

func starts(segs []segment) []int {
	out := []int{}
	for _, s := range segs {
		out = append(out, int(s.start.Sub(t0).Seconds()))
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewChecksSettingsAndCleansHalfWrittenClips(t *testing.T) {
	if _, err := New(Settings{Dir: t.TempDir(), RingLength: 15 * time.Second}, quiet); err == nil {
		t.Error("accepted a ring shorter than two segments")
	}
	dir := t.TempDir()
	clips := filepath.Join(dir, "clips")
	if err := os.MkdirAll(clips, 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(clips, string(clipA)+clipExt+partExt)
	done := filepath.Join(clips, string(clipA)+clipExt)
	for _, p := range []string{part, done} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(Settings{Dir: dir, RingLength: time.Minute}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
		t.Error("half-written clip was kept")
	}
	if _, err := os.Stat(done); err != nil {
		t.Error("finished clip was removed")
	}
}

func TestSegmentsAreListedOldestFirstAndJunkIsIgnored(t *testing.T) {
	r := newRecorder(t)
	touch(t, r, t0.Add(10*time.Second), t0.Add(20*time.Second))
	touch(t, r, t0, t0.Add(10*time.Second))
	dir := r.ringDir(dog.ID)
	for _, junk := range []string{"notes.txt", "not-a-time.mkv", ".DS_Store"} {
		if err := os.WriteFile(filepath.Join(dir, junk), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	segs := r.segments(dir)
	if !sameInts(starts(segs), []int{0, 10}) {
		t.Fatalf("starts = %v", starts(segs))
	}
	if !segs[0].end.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("end = %v", segs[0].end)
	}
	if got := r.segments(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("missing directory: %v", got)
	}
}

func TestOverlappingAndCovered(t *testing.T) {
	sec := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }
	// Three segments back to back, a gap (camera released), one more.
	segs := []segment{
		{start: sec(0), end: sec(10)}, {start: sec(10), end: sec(20)}, {start: sec(20), end: sec(27)},
		{start: sec(60), end: sec(70)},
	}
	for _, c := range []struct {
		from, to int
		want     []int
	}{
		{12, 18, []int{10}},       // inside one segment
		{5, 25, []int{0, 10, 20}}, // across boundaries
		{10, 20, []int{10}},       // exactly one: neighbours only touch
		{28, 59, []int{}},         // in the gap
		{25, 65, []int{20, 60}},   // across the gap
		{-30, -1, []int{}},        // before the ring
		{100, 200, []int{}},       // after it
		{-30, 200, []int{0, 10, 20, 60}},
	} {
		if got := starts(overlapping(segs, sec(c.from), sec(c.to))); !sameInts(got, c.want) {
			t.Errorf("%d..%d: got %v, want %v", c.from, c.to, got, c.want)
		}
	}
	if covered(nil, sec(0)) {
		t.Error("an empty ring covers nothing")
	}
	if covered(segs, sec(61)) {
		t.Error("the last segment may still be growing: 61 is not covered")
	}
	if !covered(segs, sec(60)) || !covered(segs, sec(30)) {
		t.Error("times up to the start of the last segment are covered")
	}
}

func TestPruneDeletesOnlyWhatLeftTheRing(t *testing.T) {
	r := newRecorder(t) // ring length one minute
	now := t0.Add(5 * time.Minute)
	r.now = func() time.Time { return now }
	old := touch(t, r, now.Add(-90*time.Second), now.Add(-80*time.Second))
	edge := touch(t, r, now.Add(-70*time.Second), now.Add(-59*time.Second))
	fresh := touch(t, r, now.Add(-10*time.Second), now)
	r.prune(r.ringDir(dog.ID))
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Error("old segment kept")
	}
	for _, p := range []string{edge, fresh} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed", filepath.Base(p))
		}
	}
}

func TestCutRejectsBadInput(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()
	if _, err := r.Cut(ctx, "../evil", "dog", t0, t0.Add(time.Second)); err == nil {
		t.Error("accepted a clip id that is not an id")
	}
	if _, err := r.Cut(ctx, clipA, "../evil", t0, t0.Add(time.Second)); err == nil {
		t.Error("accepted a bad camera id")
	}
	if _, err := r.Cut(ctx, clipA, "dog", t0, t0); err == nil {
		t.Error("accepted an empty span")
	}
}

func TestCutWaitsForFootageButNotForever(t *testing.T) {
	r := newRecorder(t)
	r.s.SegmentLength, r.cutGrace = 100*time.Millisecond, 100*time.Millisecond

	// Nothing is ever recorded: give up after one segment plus the margin.
	began := time.Now()
	to := began.Add(100 * time.Millisecond)
	_, err := r.Cut(context.Background(), clipA, "dog", began.Add(-time.Second), to)
	if !errors.Is(err, ports.ErrNothingRecorded) {
		t.Fatalf("err = %v, want ErrNothingRecorded", err)
	}
	if waited := time.Since(began); waited < 300*time.Millisecond || waited > 3*time.Second {
		t.Errorf("waited %s, want about 300ms", waited)
	}

	// A cancelled context ends the wait at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Cut(ctx, clipA, "dog", time.Now(), time.Now().Add(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// makeSegment writes a real two-second video file with sound.
func makeSegment(t *testing.T, bin string, r *Recorder, start time.Time) {
	t.Helper()
	dir := r.ringDir(dog.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, start.UTC().Format(nameLayout)+segmentExt)
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "2", "-c:v", "mpeg4", "-c:a", "libopus", "-y", p).CombinedOutput()
	if err != nil {
		t.Skipf("this ffmpeg cannot make the test video: %v: %s", err, out)
	}
	end := start.Add(2 * time.Second)
	if err := os.Chtimes(p, end, end); err != nil {
		t.Fatal(err)
	}
}

func TestCutJoinsSegmentsIntoAPlayableClip(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	r := newRecorder(t)
	r.s.FFmpegBin = bin
	r.s.SegmentLength = 2 * time.Second
	for i := range 4 {
		makeSegment(t, bin, r, t0.Add(time.Duration(2*i)*time.Second))
	}

	// 3 s..5 s lies in the second and third segment. The fourth exists, so
	// there is nothing to wait for.
	clip, err := r.Cut(context.Background(), clipA, "dog", t0.Add(3*time.Second), t0.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if clip.ID != clipA || clip.CameraID != "dog" || !clip.StartedAt.Equal(t0.Add(2*time.Second)) {
		t.Errorf("clip = %+v", clip)
	}
	if clip.Duration < 3900*time.Millisecond || clip.Duration > 4200*time.Millisecond {
		t.Errorf("duration = %s, want about 4s (two segments)", clip.Duration)
	}

	f, err := r.Open(clipA)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		t.Fatal(err)
	}
	size, _ := f.Seek(0, io.SeekEnd)
	f.Close()
	if string(head[4:8]) != "ftyp" {
		t.Errorf("file does not start like an MP4: %q", head)
	}
	if size != clip.SizeBytes || size == 0 {
		t.Errorf("SizeBytes = %d, file is %d", clip.SizeBytes, size)
	}
	// ffmpeg must be able to decode the whole clip.
	if out, err := exec.Command(bin, "-v", "error", "-i", r.clipPath(clipA), "-f", "null", "-").CombinedOutput(); err != nil || len(out) > 0 {
		t.Errorf("clip does not decode cleanly: %v: %s", err, out)
	}
	if left, _ := filepath.Glob(filepath.Join(r.clipsDir(), "*"+partExt)); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}

	ids, err := r.Stored()
	if err != nil || len(ids) != 1 || ids[0] != clipA {
		t.Fatalf("Stored = %v, %v", ids, err)
	}
	if err := r.Delete(clipA); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(clipA); err != nil {
		t.Errorf("deleting twice: %v", err)
	}
	if _, err := r.Open(clipA); !errors.Is(err, ports.ErrClipNotFound) {
		t.Errorf("Open after delete: %v", err)
	}
	if ids, _ := r.Stored(); len(ids) != 0 {
		t.Errorf("Stored after delete = %v", ids)
	}
}

func TestOpenAndDeleteNeverLeaveTheClipsDirectory(t *testing.T) {
	r := newRecorder(t)
	secret := filepath.Join(r.s.Dir, "secret.mp4")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.ClipID{"../secret", "", "x/y"} {
		if _, err := r.Open(id); !errors.Is(err, ports.ErrClipNotFound) {
			t.Errorf("Open(%q) = %v", id, err)
		}
		if err := r.Delete(id); err != nil {
			t.Errorf("Delete(%q) = %v", id, err)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Error("a file outside the clips directory was deleted")
	}
}

func TestRecordRestartsTheRingProcessAndStopsWithTheContext(t *testing.T) {
	r := newRecorder(t)
	r.minBackoff, r.maxBackoff, r.settle = 5*time.Millisecond, 10*time.Millisecond, time.Hour
	runs := filepath.Join(t.TempDir(), "runs")
	// A stand-in for ffmpeg that notes it ran and fails, as when the camera
	// is released.
	r.command = func(ctx context.Context, cam domain.Camera, dir string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `echo run >> "$1"; echo "no stream" >&2; exit 1`, "sh", runs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Record(ctx, dog) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(runs)
		if strings.Count(string(data), "run") >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ring process was not restarted; runs: %q", data)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Record returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Record did not return after the context ended")
	}
	if _, err := os.Stat(r.ringDir(dog.ID)); err != nil {
		t.Errorf("ring directory was not created: %v", err)
	}
}

func TestRingCommand(t *testing.T) {
	r := newRecorder(t)
	r.s.RTSPAddr = "127.0.0.1:8554"
	cmd := r.ringCommand(context.Background(), dog, "/ring/dog")
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"-i rtsp://127.0.0.1:8554/dog", "-c copy", "-f segment", "-segment_time 10",
		"-segment_format matroska", "-strftime 1", "/ring/dog/%Y%m%dT%H%M%SZ.mkv",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("command lacks %q:\n%s", want, args)
		}
	}
	if cmd.Env[len(cmd.Env)-1] != "TZ=UTC" {
		t.Error("file names must be written in UTC")
	}
}

func TestLastOutTime(t *testing.T) {
	in := "frame=10\nout_time_us=1000000\nprogress=continue\nout_time_us=4250000\nout_time_us=N/A\nprogress=end\n"
	if got := lastOutTime(strings.NewReader(in)); got != 4250*time.Millisecond {
		t.Errorf("got %s", got)
	}
	if got := lastOutTime(strings.NewReader("")); got != 0 {
		t.Errorf("empty: %s", got)
	}
}
