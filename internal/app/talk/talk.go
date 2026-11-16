// Package talk is the application service for talking through the
// loudspeaker at a camera (ADR-0010).
//
// The browser records the owner's voice and uploads it in small pieces
// ("chunks", about ten a second), each as its own HTTP request with a
// running number. This package puts the chunks in order and feeds them to
// a ports.Speaker. One stretch of talking is a session:
//
//   - Start opens the speaker. Only one session per camera: a new one ends
//     the old one (the owner picked up another device).
//   - Write adds a chunk. Chunks that arrive out of order wait for the
//     missing one; a chunk that arrives twice is ignored.
//   - Stop ends the session. A session also ends by itself when no chunk
//     arrived for a while (the phone lost its connection) or when it has
//     lasted too long.
//
// While a session is active the service reports it (OnActive), so that
// notifications can be held back: the microphone hears the owner's own
// voice as noise.
package talk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// SessionID identifies one stretch of talking. It is random and acts as the
// permission to add audio to it.
type SessionID string

var (
	// ErrDisabled: talking is switched off in the config.
	ErrDisabled = errors.New("talking is switched off on the server")
	// ErrNoSession: the session does not exist or has ended.
	ErrNoSession = errors.New("talk session not found or already ended")
	// ErrTooFarAhead: a chunk arrived whose predecessors are missing and
	// too many are already waiting.
	ErrTooFarAhead = errors.New("too many audio chunks are missing before this one")
)

// Settings are the rules.
type Settings struct {
	// Enabled turns talking on.
	Enabled bool
	// IdleTimeout ends a session when no chunk arrived for this long.
	IdleTimeout time.Duration
	// MaxDuration ends a session however active it is, so a stuck button
	// cannot keep the loudspeaker open.
	MaxDuration time.Duration
	// HoldOff is how long after the last chunk notifications stay held
	// back (the voice still echoes through the detector's timing).
	HoldOff time.Duration
}

// maxWaiting is how many out-of-order chunks one session keeps.
const maxWaiting = 16

type session struct {
	id      SessionID
	cam     domain.CameraID
	out     io.WriteCloser
	cancel  context.CancelFunc
	started time.Time
	last    time.Time
	next    int            // number of the chunk to play next
	waiting map[int][]byte // chunks that arrived early
	bytes   int64
	// writeMu keeps chunks of one session in order while they are written
	// to the speaker outside the service lock.
	writeMu sync.Mutex
	endOnce sync.Once
}

// Service implements the rules. Create with New, then Run.
type Service struct {
	speaker  ports.Speaker
	s        Settings
	logger   *slog.Logger
	now      func() time.Time
	newID    func() SessionID
	onActive []func(cam domain.CameraID, until time.Time)

	mu       sync.Mutex
	sessions map[SessionID]*session
	byCam    map[domain.CameraID]SessionID
}

// New builds the service. speaker may be nil when s.Enabled is false.
func New(speaker ports.Speaker, s Settings, logger *slog.Logger) *Service {
	if speaker == nil {
		s.Enabled = false
	}
	return &Service{
		speaker: speaker, s: s, logger: logger, now: time.Now, newID: randomID,
		sessions: map[SessionID]*session{}, byCam: map[domain.CameraID]SessionID{},
	}
}

func randomID() SessionID {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails on supported platforms (Go 1.24+)
	return SessionID(hex.EncodeToString(b[:]))
}

// Enabled reports whether talking is possible.
func (s *Service) Enabled() bool { return s.s.Enabled }

// OnActive registers fn to be called whenever the owner is talking at a
// camera, with the time until which that should be assumed. Register before
// use; fn must return quickly.
func (s *Service) OnActive(fn func(cam domain.CameraID, until time.Time)) {
	s.onActive = append(s.onActive, fn)
}

func (s *Service) active(cam domain.CameraID, now time.Time) {
	for _, fn := range s.onActive {
		fn(cam, now.Add(s.s.HoldOff))
	}
}

// Start opens a session for the camera, ending the one it may have.
func (s *Service) Start(cam domain.CameraID) (SessionID, error) {
	if !s.s.Enabled {
		return "", ErrDisabled
	}
	s.mu.Lock()
	old := s.takeLocked(s.byCam[cam])
	s.mu.Unlock()
	s.end(old, "replaced by a new session")

	// The speaker lives as long as the session, not as long as the request
	// that started it.
	ctx, cancel := context.WithCancel(context.Background())
	out, err := s.speaker.Play(ctx, cam)
	if err != nil {
		cancel()
		return "", err
	}
	now := s.now()
	sess := &session{
		id: s.newID(), cam: cam, out: out, cancel: cancel,
		started: now, last: now, waiting: map[int][]byte{},
	}
	s.mu.Lock()
	// Two starts at the same moment: the later one wins.
	raced := s.takeLocked(s.byCam[cam])
	s.sessions[sess.id] = sess
	s.byCam[cam] = sess.id
	s.mu.Unlock()
	s.end(raced, "replaced by a new session")

	s.active(cam, now)
	s.logger.Info("talk started", "camera", cam)
	return sess.id, nil
}

// Write adds chunk number seq (0, 1, 2, ...) to the session.
func (s *Service) Write(id SessionID, seq int, data []byte) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return ErrNoSession
	}
	now := s.now()
	sess.last = now
	var ready [][]byte
	switch {
	case seq < sess.next:
		// Seen before (the phone repeated a request): nothing to do.
	case seq == sess.next:
		ready = append(ready, data)
		sess.next++
		for {
			more, ok := sess.waiting[sess.next]
			if !ok {
				break
			}
			delete(sess.waiting, sess.next)
			ready = append(ready, more)
			sess.next++
		}
	default:
		if len(sess.waiting) >= maxWaiting {
			s.mu.Unlock()
			return ErrTooFarAhead
		}
		sess.waiting[seq] = data
	}
	// Taking the write lock before releasing the service lock keeps the
	// order of two calls that both have chunks ready.
	sess.writeMu.Lock()
	s.mu.Unlock()
	s.active(sess.cam, now)
	var err error
	for _, chunk := range ready {
		var n int
		n, err = sess.out.Write(chunk)
		sess.bytes += int64(n)
		if err != nil {
			break
		}
	}
	sess.writeMu.Unlock()
	if err != nil {
		// end takes the write lock itself, so it runs after the unlock.
		s.mu.Lock()
		s.takeLocked(id)
		s.mu.Unlock()
		s.end(sess, "the speaker failed")
	}
	return err
}

// Stop ends the session. Stopping one that has ended is not an error: the
// phone cannot know that it timed out.
func (s *Service) Stop(id SessionID) {
	s.mu.Lock()
	sess := s.takeLocked(id)
	s.mu.Unlock()
	s.end(sess, "stopped")
}

// takeLocked removes the session from the service and returns it, or nil.
// The caller holds s.mu and passes the result to end.
func (s *Service) takeLocked(id SessionID) *session {
	sess, ok := s.sessions[id]
	if !ok {
		return nil
	}
	delete(s.sessions, id)
	if s.byCam[sess.cam] == id {
		delete(s.byCam, sess.cam)
	}
	return sess
}

// end closes the speaker of a session that was taken out of the service.
func (s *Service) end(sess *session, reason string) {
	if sess == nil {
		return
	}
	sess.endOnce.Do(func() {
		sess.writeMu.Lock()
		err := sess.out.Close()
		bytes := sess.bytes
		sess.writeMu.Unlock()
		sess.cancel()
		attrs := []any{
			"camera", sess.cam, "reason", reason,
			"duration", s.now().Sub(sess.started).Round(100 * time.Millisecond), "kb", bytes / 1000,
		}
		if err != nil {
			s.logger.Warn("talk ended", append(attrs, "err", err)...)
			return
		}
		s.logger.Info("talk ended", attrs...)
	})
}

// expire ends the sessions that are idle or too long at the time now.
func (s *Service) expire(now time.Time) {
	type ended struct {
		sess   *session
		reason string
	}
	var out []ended
	s.mu.Lock()
	for id, sess := range s.sessions {
		switch {
		case now.Sub(sess.last) >= s.s.IdleTimeout:
			out = append(out, ended{s.takeLocked(id), "no audio arrived any more"})
		case now.Sub(sess.started) >= s.s.MaxDuration:
			out = append(out, ended{s.takeLocked(id), "longest talk time reached"})
		}
	}
	s.mu.Unlock()
	for _, e := range out {
		s.end(e.sess, e.reason)
	}
}

// Run ends stale sessions until ctx is done, then ends all of them.
func (s *Service) Run(ctx context.Context) {
	if !s.s.Enabled {
		return
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			var all []*session
			for id := range s.sessions {
				all = append(all, s.takeLocked(id))
			}
			s.mu.Unlock()
			for _, sess := range all {
				s.end(sess, "service stopping")
			}
			return
		case <-tick.C:
			s.expire(s.now())
		}
	}
}
