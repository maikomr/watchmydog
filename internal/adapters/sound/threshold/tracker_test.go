package threshold

import (
	"math"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
)

var testRules = Rules{ThresholdDBFS: -30, HysteresisDB: 6, MinDuration: time.Second, QuietTime: 3 * time.Second}

const (
	quietLevel = -50.0
	loudLevel  = -10.0
	midLevel   = -33.0 // between release (-36) and threshold (-30)
)

type seen struct {
	change transition
	span   span
	at     time.Duration // offset of the window end from t0
}

// feed runs levels (one per 100 ms window) through a tracker and returns
// the transitions it produced.
func feed(tr *tracker, t0 time.Time, startWindow int, levels ...float64) []seen {
	var out []seen
	for i, l := range levels {
		off := time.Duration(startWindow+i+1) * Window
		if ch, sp := tr.observe(t0.Add(off), Window, l); ch != none {
			out = append(out, seen{ch, sp, off})
		}
	}
	return out
}

func repeat(level float64, d time.Duration) []float64 {
	out := make([]float64, int(d/Window))
	for i := range out {
		out[i] = level
	}
	return out
}

func concat(parts ...[]float64) []float64 {
	var out []float64
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestQuietRoomProducesNothing(t *testing.T) {
	tr := newTracker(testRules)
	if got := feed(tr, time.Unix(0, 0), 0, repeat(quietLevel, 30*time.Minute)...); len(got) != 0 {
		t.Fatalf("quiet room produced %v", got)
	}
}

func TestSustainedNoiseStartsAfterMinDurationAndEndsAfterQuietTime(t *testing.T) {
	t0 := time.Unix(1000, 0)
	tr := newTracker(testRules)
	levels := concat(
		repeat(quietLevel, 2*time.Second),
		repeat(loudLevel, 4*time.Second), // 2 s .. 6 s
		repeat(quietLevel, 5*time.Second),
	)
	got := feed(tr, t0, 0, levels...)
	if len(got) != 2 || got[0].change != started || got[1].change != ended {
		t.Fatalf("want started+ended, got %+v", got)
	}
	// Reported 1 s after the noise began...
	if got[0].at != 3*time.Second {
		t.Errorf("started reported at %v, want 3s", got[0].at)
	}
	// ...but the event's start is when the noise began.
	if want := t0.Add(2 * time.Second); !got[0].span.start.Equal(want) {
		t.Errorf("start = %v, want %v", got[0].span.start, want)
	}
	// Ended 3 s of quiet after the noise stopped; the end is the last sound.
	if got[1].at != 9*time.Second {
		t.Errorf("ended reported at %v, want 9s", got[1].at)
	}
	if want := t0.Add(6 * time.Second); !got[1].span.end.Equal(want) {
		t.Errorf("end = %v, want %v", got[1].span.end, want)
	}
	if got[1].span.peak != loudLevel {
		t.Errorf("peak = %v", got[1].span.peak)
	}
}

func TestShortNoiseIsIgnored(t *testing.T) {
	tr := newTracker(testRules)
	levels := concat(repeat(loudLevel, 500*time.Millisecond), repeat(quietLevel, 10*time.Second))
	if got := feed(tr, time.Unix(0, 0), 0, levels...); len(got) != 0 {
		t.Fatalf("a 0.5 s noise must not be an event: %+v", got)
	}
	if tr.phase != idle {
		t.Errorf("tracker should have forgotten the noise, phase=%v", tr.phase)
	}
}

func TestBarksWithGapsAddUp(t *testing.T) {
	// 300 ms bark, 700 ms pause, repeated: loud time reaches 1 s during
	// the fourth bark.
	tr := newTracker(testRules)
	bark := concat(repeat(loudLevel, 300*time.Millisecond), repeat(quietLevel, 700*time.Millisecond))
	got := feed(tr, time.Unix(0, 0), 0, concat(bark, bark, bark, bark)...)
	if len(got) != 1 || got[0].change != started {
		t.Fatalf("want one started, got %+v", got)
	}
	if got[0].at != 3100*time.Millisecond {
		t.Errorf("started at %v, want 3.1s", got[0].at)
	}
}

func TestBurstsCloserThanQuietTimeAreOneEvent(t *testing.T) {
	tr := newTracker(testRules)
	levels := concat(
		repeat(loudLevel, 2*time.Second),
		repeat(quietLevel, 2*time.Second), // shorter than QuietTime
		repeat(loudLevel, 2*time.Second),
		repeat(quietLevel, 4*time.Second),
	)
	got := feed(tr, time.Unix(0, 0), 0, levels...)
	if len(got) != 2 {
		t.Fatalf("want one event (started, ended), got %+v", got)
	}
	if d := got[1].span.end.Sub(got[1].span.start); d != 6*time.Second {
		t.Errorf("event length = %v, want 6s", d)
	}
}

func TestHysteresisKeepsEventOpenBetweenReleaseAndThreshold(t *testing.T) {
	tr := newTracker(testRules)
	levels := concat(
		repeat(loudLevel, 2*time.Second),
		repeat(midLevel, 10*time.Second), // not loud, but not quiet either
	)
	got := feed(tr, time.Unix(0, 0), 0, levels...)
	if len(got) != 1 || got[0].change != started {
		t.Fatalf("event must stay open above the release level, got %+v", got)
	}
	// And the in-between level alone never starts one.
	tr = newTracker(testRules)
	if got := feed(tr, time.Unix(0, 0), 0, repeat(midLevel, 10*time.Second)...); len(got) != 0 {
		t.Fatalf("level below threshold started an event: %+v", got)
	}
}

func TestPeakTracksLoudestWindow(t *testing.T) {
	tr := newTracker(testRules)
	levels := concat(repeat(-20, time.Second), []float64{-5}, repeat(-25, time.Second), repeat(quietLevel, 4*time.Second))
	got := feed(tr, time.Unix(0, 0), 0, levels...)
	if len(got) != 2 || got[1].span.peak != -5 {
		t.Fatalf("want peak -5, got %+v", got)
	}
}

func TestOneWindowMinDurationTriggersImmediately(t *testing.T) {
	r := testRules
	r.MinDuration = Window
	tr := newTracker(r)
	got := feed(tr, time.Unix(0, 0), 0, quietLevel, loudLevel)
	if len(got) != 1 || got[0].change != started || got[0].at != 200*time.Millisecond {
		t.Fatalf("got %+v", got)
	}
}

func TestFlush(t *testing.T) {
	tr := newTracker(testRules)
	feed(tr, time.Unix(0, 0), 0, repeat(loudLevel, 2*time.Second)...)
	sp, open := tr.flush()
	if !open || sp.end.Sub(sp.start) != 2*time.Second {
		t.Fatalf("flush = %+v, %v", sp, open)
	}
	if _, open := tr.flush(); open {
		t.Error("second flush must report nothing")
	}
	// Pending (not yet an event) flushes to nothing.
	tr = newTracker(testRules)
	feed(tr, time.Unix(0, 0), 0, loudLevel)
	if _, open := tr.flush(); open {
		t.Error("pending noise must not flush as an event")
	}
}

// pcm builds n samples of a square wave with the given amplitude.
func pcm(n int, amp int16) []byte {
	out := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := amp
		if i%2 == 1 {
			v = -amp
		}
		out[2*i] = byte(uint16(v))
		out[2*i+1] = byte(uint16(v) >> 8)
	}
	return out
}

func TestRMSDBFS(t *testing.T) {
	cases := []struct {
		name string
		pcm  []byte
		want float64
	}{
		{"empty", nil, domain.SilenceDBFS},
		{"silence", pcm(1600, 0), domain.SilenceDBFS},
		{"full scale", pcm(1600, 32767), 0},
		{"half", pcm(1600, 16384), -6.02},
		{"tenth", pcm(1600, 3277), -20},
	}
	for _, c := range cases {
		if got := rmsDBFS(c.pcm); math.Abs(got-c.want) > 0.01 {
			t.Errorf("%s: got %.3f, want %.3f", c.name, got, c.want)
		}
	}
}
