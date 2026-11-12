package domain

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Notification is a message for the owner's devices.
type Notification struct {
	Title string
	Body  string
	// URL is the path the app opens when the notification is tapped.
	URL string
	// Tag groups notifications: a new one replaces an older one with the
	// same tag on the device instead of piling up.
	Tag string
	// At is when the thing being reported happened.
	At time.Time
}

// PushSubscription is one device (browser installation) that agreed to
// receive notifications. The browser creates it; its fields are defined by
// the Web Push standard (RFC 8030, RFC 8291).
type PushSubscription struct {
	// Endpoint is the URL at the browser vendor's push service that
	// delivers to this device. It identifies the subscription.
	Endpoint string
	// P256DH is the device's public key (base64url); messages are
	// encrypted to it, so the push service cannot read them.
	P256DH string
	// Auth is the device's shared authentication secret (base64url).
	Auth      string
	CreatedAt time.Time
}

const (
	maxEndpointLen = 2048
	p256dhLen      = 65 // uncompressed P-256 point
	authLen        = 16
)

// ErrInvalidSubscription is wrapped by every error Validate returns.
var ErrInvalidSubscription = errors.New("invalid subscription")

// Validate checks the shape of a subscription received from a browser.
func (s PushSubscription) Validate() error {
	if s.Endpoint == "" || len(s.Endpoint) > maxEndpointLen {
		return fmt.Errorf("%w: endpoint missing or too long", ErrInvalidSubscription)
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: endpoint must be an https URL", ErrInvalidSubscription)
	}
	if n, err := decodedLen(s.P256DH); err != nil || n != p256dhLen {
		return fmt.Errorf("%w: p256dh must be %d bytes of base64url", ErrInvalidSubscription, p256dhLen)
	}
	if n, err := decodedLen(s.Auth); err != nil || n != authLen {
		return fmt.Errorf("%w: auth must be %d bytes of base64url", ErrInvalidSubscription, authLen)
	}
	return nil
}

// decodedLen accepts base64url with or without padding, as browsers differ.
func decodedLen(v string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
	return len(b), err
}

// DailyWindow is a span of the day that repeats every day, in minutes after
// local midnight. It may cross midnight (23:00 to 07:00).
type DailyWindow struct {
	StartMin int
	EndMin   int
}

// ParseDailyWindow reads two "HH:MM" clock times.
func ParseDailyWindow(start, end string) (DailyWindow, error) {
	s, err := parseClock(start)
	if err != nil {
		return DailyWindow{}, fmt.Errorf("start: %w", err)
	}
	e, err := parseClock(end)
	if err != nil {
		return DailyWindow{}, fmt.Errorf("end: %w", err)
	}
	if s == e {
		return DailyWindow{}, errors.New("start and end must differ")
	}
	return DailyWindow{StartMin: s, EndMin: e}, nil
}

func parseClock(v string) (int, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, fmt.Errorf("%q is not a time like 23:00", v)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Contains reports whether t's clock time, in t's own time zone, falls in
// the window. The start is included, the end is not.
func (w DailyWindow) Contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if w.StartMin < w.EndMin {
		return m >= w.StartMin && m < w.EndMin
	}
	return m >= w.StartMin || m < w.EndMin
}

// String formats the window as "23:00-07:00".
func (w DailyWindow) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.StartMin/60, w.StartMin%60, w.EndMin/60, w.EndMin%60)
}
