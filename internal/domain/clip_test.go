package domain

import (
	"testing"
	"time"
)

func TestClipID(t *testing.T) {
	a, b := NewClipID(), NewClipID()
	if a == b {
		t.Fatal("two new IDs are equal")
	}
	if err := a.Validate(); err != nil {
		t.Errorf("new ID is not valid: %v", err)
	}
	for _, bad := range []ClipID{"", "abc", "../../etc/passwd", ClipID(string(a) + "0"), "ABCDEF0123456789ABCDEF0123456789"} {
		if bad.Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestClipEndedAt(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	c := Clip{StartedAt: start, Duration: 90 * time.Second}
	if got := c.EndedAt(); !got.Equal(start.Add(90 * time.Second)) {
		t.Errorf("EndedAt = %v", got)
	}
}
