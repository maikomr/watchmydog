// Package clips is the application service for recording (ADR-0009). It
// decides which parts of the recorder's ring become clips and how long
// clips are kept:
//
//   - A noise event opens a clip that begins PreRoll before the noise.
//   - The clip is cut PostRoll after the noise ended. A new noise before
//     that moment goes into the same clip, so a dog barking on and off gives
//     one clip, not ten overlapping ones.
//   - A clip is at most MaxLength long. A noise that goes on gets a second
//     clip that begins where the first ends.
//   - Old clips are deleted: by age (KeepFor) and when all clips together
//     are bigger than MaxBytes, oldest first.
//
// The recording itself and the files are the ports.Recorder's; the list of
// clips is the ports.ClipStore's. This package does no I/O of its own.
package clips

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Settings are the rules.
type Settings struct {
	// Enabled turns recording on. When off, existing clips can still be
	// listed, played and deleted.
	Enabled bool
	// PreRoll is how long before a noise a clip begins.
	PreRoll time.Duration
	// PostRoll is how long after the noise ended a clip goes on.
	PostRoll time.Duration
	// MaxLength is the longest a single clip may be.
	MaxLength time.Duration
	// MaxBytes is the most disk space all clips together may use.
	MaxBytes int64
	// KeepFor is how long a clip is kept. 0 keeps clips until the disk
	// limit removes them.
	KeepFor time.Duration
}

// RingLength is how much of the past the recorder must keep so that every
// clip these rules can ask for is still there when it is cut: the longest
// clip, plus the time until a cut happens after its end, plus a margin.
func (s Settings) RingLength() time.Duration {
	return s.MaxLength + s.PreRoll + 2*time.Minute
}

// sweepEvery is how often old clips are looked for when nothing else
// triggers it.
const sweepEvery = time.Hour

// pending is a clip that is still being collected for one camera.
type pending struct {
	// from is where the clip begins.
	from time.Time
	// open is true while a noise is going on.
	open bool
	// until is where the clip ends; only meaningful when open is false.
	until time.Time
}

// job is one clip to cut.
type job struct {
	cam      domain.CameraID
	from, to time.Time
}

// Service implements the rules. Create with New, hook EventStarted and
// EventEnded into the monitor, then Run.
type Service struct {
	rec    ports.Recorder
	store  ports.ClipStore
	s      Settings
	logger *slog.Logger
	now    func() time.Time
	newID  func() domain.ClipID

	mu      sync.Mutex
	pending map[domain.CameraID]*pending
	// sweepMu serialises the clean-ups so two of them do not delete the
	// same clips at once.
	sweepMu sync.Mutex
}

// New builds the service. Nothing runs until Run.
func New(rec ports.Recorder, store ports.ClipStore, s Settings, logger *slog.Logger) *Service {
	return &Service{
		rec: rec, store: store, s: s, logger: logger,
		now: time.Now, newID: domain.NewClipID,
		pending: map[domain.CameraID]*pending{},
	}
}

// Settings returns the rules in use.
func (s *Service) Settings() Settings { return s.s }

// EventStarted is the monitor's hook for a noise that begins. It returns at
// once.
func (s *Service) EventStarted(e domain.Event) {
	if !s.s.Enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pending[e.CameraID]; ok {
		p.open = true // still inside the post-roll of the last noise: same clip
		return
	}
	s.pending[e.CameraID] = &pending{from: e.StartedAt.Add(-s.s.PreRoll), open: true}
}

// EventEnded is the monitor's hook for a noise that ended.
func (s *Service) EventEnded(e domain.Event) {
	if !s.s.Enabled || e.EndedAt == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[e.CameraID]
	if !ok {
		// The clip was already cut at its maximum length and the noise
		// ended right at that moment; nothing more to record.
		return
	}
	p.open = false
	p.until = e.EndedAt.Add(s.s.PostRoll)
}

// due returns the clips that can be cut at the time now and forgets them.
func (s *Service) due(now time.Time) []job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []job
	for cam, p := range s.pending {
		limit := p.from.Add(s.s.MaxLength)
		switch {
		case !p.open && !now.Before(p.until):
			to := p.until
			if to.After(limit) {
				to = limit
			}
			jobs = append(jobs, job{cam: cam, from: p.from, to: to})
			delete(s.pending, cam)
		case !now.Before(limit):
			jobs = append(jobs, job{cam: cam, from: p.from, to: limit})
			if p.open {
				p.from = limit // the noise goes on: next clip continues here
			} else {
				delete(s.pending, cam) // only post-roll was left
			}
		}
	}
	return jobs
}

// Run records every camera and cuts clips until ctx is done. It returns
// when the recorders and the cuts in progress have stopped. Clips still
// being collected at that moment are lost.
func (s *Service) Run(ctx context.Context, cameras []domain.Camera) {
	s.reconcile(ctx)
	s.sweep(ctx)
	if !s.s.Enabled {
		return
	}

	var wg sync.WaitGroup
	for _, cam := range cameras {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.rec.Record(ctx, cam); err != nil {
				s.logger.Error("recorder stopped", "camera", cam.ID, "err", err)
			}
		}()
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastSweep := s.now()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
		}
		now := s.now()
		for _, j := range s.due(now) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.cut(ctx, j)
			}()
		}
		if now.Sub(lastSweep) >= sweepEvery {
			lastSweep = now
			s.sweep(ctx)
		}
	}
	wg.Wait()
	s.mu.Lock()
	lost := len(s.pending)
	s.mu.Unlock()
	if lost > 0 {
		s.logger.Warn("stopping with a clip still being collected; it is lost", "count", lost)
	}
}

// cut makes one clip, adds it to the list and makes room if needed.
func (s *Service) cut(ctx context.Context, j job) {
	id := s.newID()
	clip, err := s.rec.Cut(ctx, id, j.cam, j.from, j.to)
	switch {
	case errors.Is(err, ports.ErrNothingRecorded):
		s.logger.Warn("no clip: nothing was recorded at that time", "camera", j.cam, "from", j.from, "to", j.to)
		return
	case err != nil:
		if ctx.Err() == nil {
			s.logger.Error("could not cut clip", "camera", j.cam, "err", err)
		}
		return
	}
	// The file exists now; if the list cannot be written, remove the file
	// again, otherwise it would use disk space nobody can see.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.SaveClip(sctx, clip); err != nil {
		s.logger.Error("could not store clip; deleting its file", "clip", id, "err", err)
		_ = s.rec.Delete(id)
		return
	}
	s.logger.Info("clip saved", "camera", clip.CameraID, "clip", clip.ID,
		"duration", clip.Duration.Round(time.Second), "mb", clip.SizeBytes/1_000_000)
	s.sweep(sctx)
}

// sweep deletes clips that are too old and, oldest first, as many as needed
// to get under the disk limit.
func (s *Service) sweep(ctx context.Context) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	all, err := s.store.ListClips(ctx, "", 0) // newest first
	if err != nil {
		s.logger.Error("clean-up: could not list clips", "err", err)
		return
	}
	var total int64
	for _, c := range all {
		total += c.SizeBytes
	}
	now := s.now()
	for i := len(all) - 1; i >= 0; i-- {
		c := all[i]
		reason := ""
		switch {
		case s.s.KeepFor > 0 && now.Sub(c.EndedAt()) > s.s.KeepFor:
			reason = "older than recording.keep_for"
		case s.s.MaxBytes > 0 && total > s.s.MaxBytes:
			reason = "over recording.max_disk_mb"
		default:
			continue
		}
		if err := s.remove(ctx, c.ID); err != nil {
			s.logger.Error("clean-up: could not delete clip", "clip", c.ID, "err", err)
			continue
		}
		total -= c.SizeBytes
		s.logger.Info("clip deleted", "clip", c.ID, "reason", reason, "recorded", c.StartedAt)
	}
}

// reconcile makes the list and the files agree after a crash: a file
// without an entry is deleted, and so is an entry without a file.
func (s *Service) reconcile(ctx context.Context) {
	files, err := s.rec.Stored()
	if err != nil {
		s.logger.Error("could not list clip files", "err", err)
		return
	}
	listed, err := s.store.ListClips(ctx, "", 0)
	if err != nil {
		s.logger.Error("could not list clips", "err", err)
		return
	}
	hasFile := map[domain.ClipID]bool{}
	for _, id := range files {
		hasFile[id] = true
	}
	for _, c := range listed {
		if hasFile[c.ID] {
			delete(hasFile, c.ID)
			continue
		}
		s.logger.Warn("clip has no file; removing it from the list", "clip", c.ID)
		if err := s.store.DeleteClip(ctx, c.ID); err != nil {
			s.logger.Error("could not remove clip from the list", "clip", c.ID, "err", err)
		}
	}
	for id := range hasFile {
		s.logger.Warn("clip file is not in the list; deleting it", "clip", id)
		if err := s.rec.Delete(id); err != nil {
			s.logger.Error("could not delete clip file", "clip", id, "err", err)
		}
	}
}

// remove deletes the file first: if that fails the clip stays listed and
// can be deleted again, instead of a hidden file using disk space.
func (s *Service) remove(ctx context.Context, id domain.ClipID) error {
	if err := s.rec.Delete(id); err != nil {
		return err
	}
	return s.store.DeleteClip(ctx, id)
}

// List returns clips, newest first, and the disk space all clips use.
func (s *Service) List(ctx context.Context, cam domain.CameraID, limit int) (clips []domain.Clip, usedBytes int64, err error) {
	all, err := s.store.ListClips(ctx, "", 0)
	if err != nil {
		return nil, 0, err
	}
	clips = []domain.Clip{}
	for _, c := range all {
		usedBytes += c.SizeBytes
		if (cam == "" || c.CameraID == cam) && (limit <= 0 || len(clips) < limit) {
			clips = append(clips, c)
		}
	}
	return clips, usedBytes, nil
}

// Open returns a clip and its video. The caller closes the file.
func (s *Service) Open(ctx context.Context, id domain.ClipID) (domain.Clip, ports.ClipFile, error) {
	clip, err := s.store.GetClip(ctx, id)
	if err != nil {
		return domain.Clip{}, nil, err
	}
	f, err := s.rec.Open(id)
	if err != nil {
		return domain.Clip{}, nil, err
	}
	return clip, f, nil
}

// Delete removes a clip. ports.ErrClipNotFound if there is none.
func (s *Service) Delete(ctx context.Context, id domain.ClipID) error {
	if _, err := s.store.GetClip(ctx, id); err != nil {
		return err
	}
	return s.remove(ctx, id)
}
