package domain

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func validSub() PushSubscription {
	return PushSubscription{
		Endpoint: "https://push.example/send/abc",
		P256DH:   base64.RawURLEncoding.EncodeToString(make([]byte, 65)),
		Auth:     base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	}
}

func TestPushSubscriptionValidate(t *testing.T) {
	if err := validSub().Validate(); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	padded := validSub()
	padded.Auth = base64.URLEncoding.EncodeToString(make([]byte, 16)) // with "=="
	if err := padded.Validate(); err != nil {
		t.Errorf("padded base64 rejected: %v", err)
	}

	cases := map[string]func(*PushSubscription){
		"no endpoint":   func(s *PushSubscription) { s.Endpoint = "" },
		"http endpoint": func(s *PushSubscription) { s.Endpoint = "http://push.example/x" },
		"no host":       func(s *PushSubscription) { s.Endpoint = "https:///x" },
		"long endpoint": func(s *PushSubscription) { s.Endpoint = "https://push.example/" + strings.Repeat("a", 2100) },
		"short p256dh":  func(s *PushSubscription) { s.P256DH = "AAAA" },
		"bad p256dh":    func(s *PushSubscription) { s.P256DH = "***" },
		"short auth":    func(s *PushSubscription) { s.Auth = "AAAA" },
		"empty auth":    func(s *PushSubscription) { s.Auth = "" },
	}
	for name, change := range cases {
		s := validSub()
		change(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDailyWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 12, h, m, 0, 0, time.UTC) }

	night, err := ParseDailyWindow("23:00", "07:00")
	if err != nil {
		t.Fatal(err)
	}
	if night.String() != "23:00-07:00" {
		t.Errorf("String() = %q", night.String())
	}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{
		{at(22, 59), false}, {at(23, 0), true}, {at(0, 0), true}, {at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false},
	} {
		if got := night.Contains(c.t); got != c.want {
			t.Errorf("night.Contains(%s) = %v", c.t.Format("15:04"), got)
		}
	}

	nap, err := ParseDailyWindow("13:30", "15:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{
		{at(13, 29), false}, {at(13, 30), true}, {at(14, 59), true}, {at(15, 0), false}, {at(2, 0), false},
	} {
		if got := nap.Contains(c.t); got != c.want {
			t.Errorf("nap.Contains(%s) = %v", c.t.Format("15:04"), got)
		}
	}

	// The clock is read in the time's own zone.
	tokyo := time.FixedZone("JST", 9*3600)
	if !night.Contains(time.Date(2026, 11, 12, 23, 30, 0, 0, tokyo)) {
		t.Error("23:30 JST should be inside 23:00-07:00")
	}

	for _, bad := range [][2]string{{"25:00", "07:00"}, {"23:00", "7"}, {"", ""}, {"08:00", "08:00"}} {
		if _, err := ParseDailyWindow(bad[0], bad[1]); err == nil {
			t.Errorf("ParseDailyWindow(%q, %q) accepted", bad[0], bad[1])
		}
	}
}
