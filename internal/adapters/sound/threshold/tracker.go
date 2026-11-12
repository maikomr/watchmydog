package threshold

import "time"

// Rules decide when measured levels become an event. See ADR-0007.
type Rules struct {
	// ThresholdDBFS: a window at or above this level is "loud".
	ThresholdDBFS float64
	// HysteresisDB: once something is loud, the level has to drop this far
	// below the threshold to count as "quiet" again. Without it, a sound
	// hovering around the threshold would flicker between the two.
	HysteresisDB float64
	// MinDuration: how much loud time is needed before an event starts.
	// Loud windows do not have to be consecutive (barks have gaps), but a
	// quiet gap of QuietTime forgets them.
	MinDuration time.Duration
	// QuietTime: how long it has to stay quiet before an event ends. Bursts
	// closer together than this are one event.
	QuietTime time.Duration
}

// ReleaseDBFS is the level below which a window counts as quiet.
func (r Rules) ReleaseDBFS() float64 { return r.ThresholdDBFS - r.HysteresisDB }

type phase int

const (
	idle    phase = iota // nothing loud recently
	pending              // heard something loud, not long enough yet
	active               // event open
)

// span is the time range and peak of a noise.
type span struct {
	start time.Time
	// end is the end of the last window that was not quiet.
	end  time.Time
	peak float64
}

// transition is what one window changed.
type transition int

const (
	none transition = iota
	started
	ended
)

// tracker is the detection state machine. It is fed one level per window
// and is pure: no clock, no I/O, so every rule is unit-tested with made-up
// levels.
//
//	idle ──loud──► pending ──loud time ≥ MinDuration──► active
//	  ▲               │                                    │
//	  └── quiet for QuietTime ◄────────────────────────────┘ (event ends)
type tracker struct {
	rules Rules
	phase phase
	cur   span
	loud  time.Duration // accumulated loud time while pending
	quiet time.Duration // current run of quiet time
}

func newTracker(r Rules) *tracker { return &tracker{rules: r} }

// observe feeds one window that ended at `at`, lasted `window` and measured
// `dbfs`. When the result is started or ended, the returned span describes
// the event.
func (t *tracker) observe(at time.Time, window time.Duration, dbfs float64) (transition, span) {
	isLoud := dbfs >= t.rules.ThresholdDBFS
	isQuiet := dbfs < t.rules.ReleaseDBFS()

	if t.phase == idle {
		if !isLoud {
			return none, span{}
		}
		t.phase = pending
		t.cur = span{start: at.Add(-window), end: at, peak: dbfs}
		t.loud, t.quiet = 0, 0
	}

	if isQuiet {
		t.quiet += window
	} else {
		t.quiet = 0
		t.cur.end = at
		t.cur.peak = max(t.cur.peak, dbfs)
	}
	if isLoud {
		t.loud += window
	}

	switch t.phase {
	case pending:
		if t.loud >= t.rules.MinDuration {
			t.phase = active
			return started, t.cur
		}
		if t.quiet >= t.rules.QuietTime {
			t.phase = idle
		}
	case active:
		if t.quiet >= t.rules.QuietTime {
			t.phase = idle
			return ended, t.cur
		}
	}
	return none, span{}
}

// isActive reports whether an event is open.
func (t *tracker) isActive() bool { return t.phase == active }

// flush ends whatever is in progress, for when the audio is lost. It
// returns the open event's span and true if there was one.
func (t *tracker) flush() (span, bool) {
	wasActive := t.phase == active
	t.phase = idle
	return t.cur, wasActive
}
