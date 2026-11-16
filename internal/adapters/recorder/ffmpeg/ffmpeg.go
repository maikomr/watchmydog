// Package ffmpeg implements ports.Recorder with ffmpeg and plain files
// (ADR-0009).
//
// The ring: one ffmpeg per camera reads the stream from go2rtc over RTSP
// and writes it, without re-encoding, as a chain of short files
// ("segments", 10 s each) named after the time they begin. Segments older
// than the ring length are deleted.
//
//	<dir>/ring/<camera>/20261116T193041Z.mkv
//	<dir>/clips/<clip id>.mp4
//
// A clip is made by joining the segments that overlap the wanted time span
// into one MP4, again without re-encoding. That is why a clip starts and
// ends on segment boundaries and can be up to one segment longer at each
// end than asked.
//
// ffmpeg only talks to go2rtc on localhost; it never opens the camera, so
// it needs no macOS permission of its own.
package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

const (
	// nameLayout is the segment file name: the UTC time the segment begins.
	nameLayout = "20060102T150405Z"
	segmentExt = ".mkv"
	clipExt    = ".mp4"
	partExt    = ".part"
)

// Settings configure the recorder.
type Settings struct {
	// FFmpegBin is the ffmpeg executable.
	FFmpegBin string
	// RTSPAddr is go2rtc's RTSP listener (host:port).
	RTSPAddr string
	// Dir is where the ring and the clips are kept.
	Dir string
	// RingLength is how much of the past is kept per camera.
	RingLength time.Duration
	// SegmentLength is the length of one ring file. Default 10 s. The
	// stream has a key frame every 2 s and a file can only begin at one,
	// so the real length varies by up to that much.
	SegmentLength time.Duration
}

// Recorder implements ports.Recorder.
type Recorder struct {
	s      Settings
	logger *slog.Logger
	now    func() time.Time
	// command builds the ring process for a camera writing into dir.
	command func(ctx context.Context, cam domain.Camera, dir string) *exec.Cmd
	// minBackoff/maxBackoff bound the wait between reconnects.
	minBackoff, maxBackoff time.Duration
	// settle is how long the ring process must run before it counts as
	// recording (and is logged as such).
	settle time.Duration
	// cutGrace is added to one segment length when Cut waits for footage.
	cutGrace time.Duration
	// poll is how often Cut looks for new segments while waiting.
	poll time.Duration
}

var _ ports.Recorder = (*Recorder)(nil)

// New prepares the directories and removes clips that were half written
// when the service last stopped.
func New(s Settings, logger *slog.Logger) (*Recorder, error) {
	if s.SegmentLength <= 0 {
		s.SegmentLength = 10 * time.Second
	}
	if s.RingLength < 2*s.SegmentLength {
		return nil, fmt.Errorf("recorder: ring length %s is shorter than two segments", s.RingLength)
	}
	r := &Recorder{
		s: s, logger: logger, now: time.Now,
		minBackoff: time.Second, maxBackoff: 5 * time.Second,
		settle: 3 * time.Second, cutGrace: 5 * time.Second, poll: 500 * time.Millisecond,
	}
	r.command = r.ringCommand
	for _, d := range []string{r.clipsDir(), filepath.Join(s.Dir, "ring")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("recorder: %w", err)
		}
	}
	parts, _ := filepath.Glob(filepath.Join(r.clipsDir(), "*"+partExt))
	for _, p := range parts {
		_ = os.Remove(p)
	}
	return r, nil
}

func (r *Recorder) clipsDir() string { return filepath.Join(r.s.Dir, "clips") }

func (r *Recorder) ringDir(cam domain.CameraID) string {
	return filepath.Join(r.s.Dir, "ring", string(cam))
}

func (r *Recorder) clipPath(id domain.ClipID) string {
	return filepath.Join(r.clipsDir(), string(id)+clipExt)
}

// --- ring -------------------------------------------------------------------

// ringCommand reads the camera's stream from go2rtc and writes segments.
func (r *Recorder) ringCommand(ctx context.Context, cam domain.Camera, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.s.FFmpegBin,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-rtsp_transport", "tcp",
		"-timeout", "5000000", // µs without data before ffmpeg gives up
		"-i", fmt.Sprintf("rtsp://%s/%s", r.s.RTSPAddr, cam.ID),
		"-map", "0", "-c", "copy", // every track, as it is: no re-encoding
		"-f", "segment",
		"-segment_time", strconv.FormatFloat(r.s.SegmentLength.Seconds(), 'f', -1, 64),
		"-segment_atclocktime", "1", // split at :00, :10, ... of the clock
		"-segment_format", "matroska",
		"-reset_timestamps", "1",
		"-strftime", "1",
		filepath.Join(dir, "%Y%m%dT%H%M%SZ"+segmentExt),
	)
	// strftime uses the process's time zone; the names are defined as UTC.
	cmd.Env = append(os.Environ(), "TZ=UTC")
	return cmd
}

// Record implements ports.Recorder.
func (r *Recorder) Record(ctx context.Context, cam domain.Camera) error {
	dir := r.ringDir(cam.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("recorder: %w", err)
	}

	// Throw away old segments for as long as we record, and also while the
	// stream is down, so a released camera does not leave stale footage.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(r.s.SegmentLength)
		defer t.Stop()
		for {
			r.prune(dir)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	defer wg.Wait()

	backoff := r.minBackoff
	warned := false
	for ctx.Err() == nil {
		recording, err := r.runRing(ctx, cam, dir)
		if ctx.Err() != nil {
			break
		}
		if recording {
			warned = false
			backoff = r.minBackoff
		}
		// Log the loss once, not every retry: while the camera is released
		// this loop fails every few seconds by design.
		if !warned {
			warned = true
			r.logger.Warn("recording unavailable, retrying", "camera", cam.ID, "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, r.maxBackoff)
	}
	return nil
}

// runRing runs one ring process until it ends. recording reports whether it
// ran long enough to count as working.
func (r *Recorder) runRing(ctx context.Context, cam domain.Camera, dir string) (recording bool, err error) {
	cmd := r.command(ctx, cam, dir)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	stderr := &tailBuffer{max: 400}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start ffmpeg: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	settled := time.NewTimer(r.settle)
	defer settled.Stop()
	for {
		select {
		case <-settled.C:
			recording = true
			r.logger.Info("recording", "camera", cam.ID)
		case werr := <-done:
			if msg := stderr.String(); msg != "" {
				return recording, errors.New("ffmpeg: " + msg)
			}
			if werr != nil {
				return recording, fmt.Errorf("ffmpeg: %w", werr)
			}
			return recording, errors.New("ffmpeg: stream ended")
		}
	}
}

// segment is one file of the ring.
type segment struct {
	path string
	// start is when the segment begins (from its name).
	start time.Time
	// end is when it was last written: its end once it is complete.
	end time.Time
}

// segments lists the ring of a camera, oldest first. Files that do not look
// like segments are ignored.
func (r *Recorder) segments(dir string) []segment {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []segment
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, segmentExt) {
			continue
		}
		start, err := time.Parse(nameLayout, strings.TrimSuffix(name, segmentExt))
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // deleted in the meantime
		}
		out = append(out, segment{path: filepath.Join(dir, name), start: start.UTC(), end: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}

// prune deletes the segments that ended longer ago than the ring length.
func (r *Recorder) prune(dir string) {
	limit := r.now().Add(-r.s.RingLength)
	for _, seg := range r.segments(dir) {
		if seg.end.Before(limit) {
			_ = os.Remove(seg.path)
		}
	}
}

// --- clips ------------------------------------------------------------------

// overlapping returns the segments that hold footage between from and to.
func overlapping(segs []segment, from, to time.Time) []segment {
	var out []segment
	for _, s := range segs {
		if s.start.Before(to) && s.end.After(from) {
			out = append(out, s)
		}
	}
	return out
}

// covered reports whether everything before t is in finished segments: a
// segment that begins at or after t exists, so the one before it is closed.
func covered(segs []segment, t time.Time) bool {
	return len(segs) > 0 && !segs[len(segs)-1].start.Before(t)
}

// Cut implements ports.Recorder.
func (r *Recorder) Cut(ctx context.Context, id domain.ClipID, cam domain.CameraID, from, to time.Time) (domain.Clip, error) {
	if err := id.Validate(); err != nil {
		return domain.Clip{}, err
	}
	if err := cam.Validate(); err != nil {
		return domain.Clip{}, err
	}
	if !to.After(from) {
		return domain.Clip{}, fmt.Errorf("cut: empty time span %s to %s", from, to)
	}
	dir := r.ringDir(cam)

	// Wait until the footage up to `to` is in finished segments. If the
	// recording has stopped (camera released, service shutting down) that
	// never happens, so stop waiting after one segment plus a margin and
	// take what is there.
	deadline := to.Add(r.s.SegmentLength + r.cutGrace)
	for !covered(r.segments(dir), to) && r.now().Before(deadline) {
		select {
		case <-ctx.Done():
			return domain.Clip{}, ctx.Err()
		case <-time.After(r.poll):
		}
	}
	parts := overlapping(r.segments(dir), from, to)
	if len(parts) == 0 {
		return domain.Clip{}, ports.ErrNothingRecorded
	}

	// The list file for ffmpeg's "concat" reader: one line per segment.
	list, err := os.CreateTemp(r.clipsDir(), "join-*.txt"+partExt)
	if err != nil {
		return domain.Clip{}, fmt.Errorf("cut: %w", err)
	}
	defer os.Remove(list.Name())
	for _, p := range parts {
		fmt.Fprintf(list, "file '%s'\n", strings.ReplaceAll(p.path, "'", `'\''`))
	}
	if err := list.Close(); err != nil {
		return domain.Clip{}, fmt.Errorf("cut: %w", err)
	}

	final := r.clipPath(id)
	tmp := final + partExt
	defer os.Remove(tmp) // no-op after the rename
	cmd := exec.CommandContext(ctx, r.s.FFmpegBin,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-nostats",
		"-progress", "pipe:1", // machine-readable progress, for the duration
		"-f", "concat", "-safe", "0", "-i", list.Name(),
		"-map", "0", "-c", "copy",
		// faststart puts the index at the front of the file, so a browser
		// can start playing before it has downloaded everything.
		"-movflags", "+faststart",
		"-f", "mp4", "-y", tmp,
	)
	stderr := &tailBuffer{max: 400}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return domain.Clip{}, fmt.Errorf("cut: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return domain.Clip{}, fmt.Errorf("cut: start ffmpeg: %w", err)
	}
	duration := lastOutTime(stdout)
	if err := cmd.Wait(); err != nil {
		return domain.Clip{}, fmt.Errorf("cut: ffmpeg: %w: %s", err, stderr.String())
	}
	info, err := os.Stat(tmp)
	if err != nil || info.Size() == 0 {
		return domain.Clip{}, fmt.Errorf("cut: ffmpeg wrote no file: %s", stderr.String())
	}
	if err := os.Rename(tmp, final); err != nil {
		return domain.Clip{}, fmt.Errorf("cut: %w", err)
	}
	if duration <= 0 {
		// ffmpeg did not say; the file times are close enough.
		duration = parts[len(parts)-1].end.Sub(parts[0].start)
	}
	return domain.Clip{
		ID: id, CameraID: cam,
		StartedAt: parts[0].start, Duration: duration.Round(100 * time.Millisecond), SizeBytes: info.Size(),
	}, nil
}

// lastOutTime reads ffmpeg's -progress output ("key=value" lines) to the end
// and returns the last reported output position.
func lastOutTime(r io.Reader) time.Duration {
	var last time.Duration
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		// out_time_us is in microseconds ("out_time_ms" is too, a known
		// ffmpeg quirk, so it is not used).
		if v, ok := strings.CutPrefix(sc.Text(), "out_time_us="); ok {
			if us, err := strconv.ParseInt(v, 10, 64); err == nil && us > 0 {
				last = time.Duration(us) * time.Microsecond
			}
		}
	}
	return last
}

// Open implements ports.Recorder.
func (r *Recorder) Open(id domain.ClipID) (ports.ClipFile, error) {
	// Validate first: the ID becomes part of a file path.
	if id.Validate() != nil {
		return nil, ports.ErrClipNotFound
	}
	f, err := os.Open(r.clipPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ports.ErrClipNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open clip: %w", err)
	}
	return f, nil
}

// Delete implements ports.Recorder.
func (r *Recorder) Delete(id domain.ClipID) error {
	if id.Validate() != nil {
		return nil
	}
	if err := os.Remove(r.clipPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete clip: %w", err)
	}
	return nil
}

// Stored implements ports.Recorder.
func (r *Recorder) Stored() ([]domain.ClipID, error) {
	entries, err := os.ReadDir(r.clipsDir())
	if err != nil {
		return nil, fmt.Errorf("list clips: %w", err)
	}
	out := []domain.ClipID{}
	for _, e := range entries {
		id := domain.ClipID(strings.TrimSuffix(e.Name(), clipExt))
		if !e.IsDir() && strings.HasSuffix(e.Name(), clipExt) && id.Validate() == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(t.buf)), " ")
}
