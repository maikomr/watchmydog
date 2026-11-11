package sharedsecret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/ports"
)

const testSecret = "correct-horse-battery"

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newAuth(t *testing.T, c *clock) *Authenticator {
	t.Helper()
	a, err := New(testSecret, time.Hour, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLoginAndVerify(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	a := newAuth(t, c)
	ctx := context.Background()

	s, err := a.Login(ctx, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if s.Principal.Subject != "owner" || !s.ExpiresAt.Equal(c.t.Add(time.Hour)) {
		t.Errorf("session = %+v", s)
	}
	if !strings.HasPrefix(s.Token, "v1.") || strings.Count(s.Token, ".") != 2 {
		t.Errorf("token format: %q", s.Token)
	}
	p, err := a.Verify(ctx, s.Token)
	if err != nil || p.Subject != "owner" {
		t.Errorf("verify = %+v, %v", p, err)
	}
}

func TestLoginRejectsWrongSecret(t *testing.T) {
	a := newAuth(t, &clock{t: time.Now()})
	for _, bad := range []string{"", "correct-horse-batter", "correct-horse-battery ", "CORRECT-HORSE-BATTERY"} {
		if _, err := a.Login(context.Background(), bad); !errors.Is(err, ports.ErrInvalidCredentials) {
			t.Errorf("secret %q: err = %v", bad, err)
		}
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	a := newAuth(t, c)
	s, _ := a.Login(context.Background(), testSecret)

	c.t = c.t.Add(59 * time.Minute)
	if _, err := a.Verify(context.Background(), s.Token); err != nil {
		t.Errorf("token should still be valid: %v", err)
	}
	c.t = c.t.Add(time.Minute)
	if _, err := a.Verify(context.Background(), s.Token); !errors.Is(err, ports.ErrInvalidSession) {
		t.Errorf("token should be expired, got %v", err)
	}
}

func TestVerifyRejectsTamperedTokens(t *testing.T) {
	c := &clock{t: time.Now()}
	a := newAuth(t, c)
	s, _ := a.Login(context.Background(), testSecret)
	parts := strings.Split(s.Token, ".")

	// Forge claims with a far expiry, keep the old signature.
	forged := b64.EncodeToString([]byte(`{"sub":"owner","iat":0,"exp":9999999999}`))
	cases := map[string]string{
		"empty":         "",
		"garbage":       "not-a-token",
		"wrong version": "v2." + parts[1] + "." + parts[2],
		"forged claims": parts[0] + "." + forged + "." + parts[2],
		"bad signature": parts[0] + "." + parts[1] + ".AAAA",
		"bad base64":    parts[0] + ".%%%." + parts[2],
		"extra part":    s.Token + ".x",
	}
	for name, tok := range cases {
		if _, err := a.Verify(context.Background(), tok); !errors.Is(err, ports.ErrInvalidSession) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestChangingSecretInvalidatesSessions(t *testing.T) {
	c := &clock{t: time.Now()}
	a := newAuth(t, c)
	s, _ := a.Login(context.Background(), testSecret)

	b, err := New("a-completely-different-secret", time.Hour, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify(context.Background(), s.Token); !errors.Is(err, ports.ErrInvalidSession) {
		t.Errorf("token from old secret must be rejected, got %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New("short", time.Hour, nil); err == nil {
		t.Error("short secret must be rejected")
	}
	if _, err := New(testSecret, 0, nil); err == nil {
		t.Error("zero ttl must be rejected")
	}
}

func TestGenerateSecret(t *testing.T) {
	re := regexp.MustCompile(`^[a-hjkmnp-z2-9]{5}(-[a-hjkmnp-z2-9]{5}){3}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		s, err := GenerateSecret()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(s) {
			t.Fatalf("unexpected format %q", s)
		}
		if seen[s] {
			t.Fatalf("duplicate secret %q", s)
		}
		seen[s] = true
	}
	// A generated secret must be accepted by New.
	s, _ := GenerateSecret()
	if _, err := New(s, time.Hour, nil); err != nil {
		t.Errorf("generated secret rejected: %v", err)
	}
}

func TestCreateSecretFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := CreateSecretFile(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(p)
	if len(strings.TrimSpace(string(data))) != 23 {
		t.Errorf("unexpected content length %d", len(data))
	}
	if err := CreateSecretFile(p); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second create must refuse to overwrite, got %v", err)
	}
}
