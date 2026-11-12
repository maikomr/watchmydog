package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/app/alerts"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

const goodSecret = "correct-horse-battery"

// fakeAuth accepts goodSecret and the single token it issues.
type fakeAuth struct{ now func() time.Time }

func (f fakeAuth) Login(_ context.Context, secret string) (ports.Session, error) {
	if secret != goodSecret {
		return ports.Session{}, ports.ErrInvalidCredentials
	}
	return ports.Session{Token: "tok-1", Principal: ports.Principal{Subject: "owner"}, ExpiresAt: f.now().Add(30 * 24 * time.Hour)}, nil
}

func (f fakeAuth) Verify(_ context.Context, token string) (ports.Principal, error) {
	if token != "tok-1" {
		return ports.Principal{}, ports.ErrInvalidSession
	}
	return ports.Principal{Subject: "owner"}, nil
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

type authFixture struct {
	h     http.Handler
	clock *testClock
	eng   *fakeEngine
	logs  *bytes.Buffer
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o644)
	clock := &testClock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	eng := newEngine()
	logs := &bytes.Buffer{}
	h := NewHandler(Deps{
		Logger:           slog.New(slog.NewTextHandler(logs, nil)),
		Now:              clock.now,
		Cameras:          []domain.Camera{dog()},
		Engine:           eng,
		Monitor:          newMonitor(memory.New()),
		Alerts:           newAlerts(memory.New(), &recNotifier{}, alerts.Settings{Enabled: true, PublicKey: "pub"}),
		WebDir:           dir,
		Auth:             fakeAuth{now: clock.now},
		LoginMaxFailures: 3,
		LoginWindow:      time.Minute,
	})
	return &authFixture{h: h, clock: clock, eng: eng, logs: logs}
}

// req performs a request with optional JSON body, cookie and headers.
func (f *authFixture) req(method, path, body, cookie string, headers ...string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	return rr
}

func (f *authFixture) login(secret string, headers ...string) *httptest.ResponseRecorder {
	return f.req(http.MethodPost, "/api/login", `{"secret":"`+secret+`"}`, "", headers...)
}

func TestProtectedRoutesRequireSession(t *testing.T) {
	f := newAuthFixture(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/cameras"},
		{http.MethodGet, "/api/cameras/dog"},
		{http.MethodPost, "/api/cameras/dog/pause"},
		{http.MethodPost, "/api/cameras/dog/resume"},
		{http.MethodGet, "/api/events"},
		{http.MethodGet, "/api/events/stream"},
		{http.MethodGet, "/api/push/config"},
		{http.MethodPost, "/api/push/subscribe"},
		{http.MethodPost, "/api/push/unsubscribe"},
		{http.MethodPost, "/api/push/test"},
		{http.MethodGet, "/media/api/webrtc?src=dog"},
		{http.MethodPost, "/media/api/webrtc?src=dog"},
	} {
		for _, cookie := range []string{"", "forged-token"} {
			rr := f.req(tc.method, tc.path, "", cookie)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s cookie=%q: status %d, want 401", tc.method, tc.path, cookie, rr.Code)
			}
		}
	}
	if len(f.eng.calls) != 0 {
		t.Errorf("engine must not be reached without a session: %v", f.eng.calls)
	}
}

func TestPublicRoutes(t *testing.T) {
	f := newAuthFixture(t)
	if rr := f.req(http.MethodGet, "/api/health", "", ""); rr.Code != http.StatusOK {
		t.Errorf("health = %d", rr.Code)
	}
	if rr := f.req(http.MethodGet, "/", "", ""); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "app") {
		t.Errorf("web client must load without a session to show the login screen: %d", rr.Code)
	}
	if rr := f.req(http.MethodGet, "/api/session", "", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("session without cookie = %d, want 401", rr.Code)
	}
}

func TestLoginSetsHardenedCookieAndUnlocksAPI(t *testing.T) {
	f := newAuthFixture(t)
	rr := f.login(goodSecret)
	if rr.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rr.Code, rr.Body.String())
	}
	var body sessionResponse
	decode(t, rr, &body)
	if !body.Authenticated || !body.AuthRequired || body.Subject != "owner" || body.ExpiresAt == nil {
		t.Errorf("login body = %+v", body)
	}

	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != sessionCookieName || c.Value != "tok-1" || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie must be HttpOnly, Secure, SameSite=Lax: %+v", c)
	}
	if c.MaxAge != int((30 * 24 * time.Hour).Seconds()) {
		t.Errorf("MaxAge = %d", c.MaxAge)
	}

	if rr := f.req(http.MethodGet, "/api/cameras", "", "tok-1"); rr.Code != http.StatusOK {
		t.Errorf("cameras with session = %d", rr.Code)
	}
	if rr := f.req(http.MethodGet, "/media/api/webrtc?src=dog", "", "tok-1"); rr.Code != http.StatusOK {
		t.Errorf("media with session = %d", rr.Code)
	}
	rr = f.req(http.MethodGet, "/api/session", "", "tok-1")
	decode(t, rr, &body)
	if rr.Code != http.StatusOK || body.Subject != "owner" {
		t.Errorf("session with cookie = %d %+v", rr.Code, body)
	}

	// Pause/resume is attributed to the logged-in principal in the log.
	if rr := f.req(http.MethodPost, "/api/cameras/dog/pause", "", "tok-1"); rr.Code != http.StatusOK {
		t.Fatalf("pause = %d", rr.Code)
	}
	if !strings.Contains(f.logs.String(), `msg="camera state changed" camera=dog paused=true by=owner`) {
		t.Errorf("audit log line missing:\n%s", f.logs.String())
	}
}

func TestLoginRejectsWrongSecretAndBadBodies(t *testing.T) {
	f := newAuthFixture(t)
	rr := f.login("nope")
	if rr.Code != http.StatusUnauthorized || len(rr.Result().Cookies()) != 0 {
		t.Errorf("wrong secret = %d cookies=%v", rr.Code, rr.Result().Cookies())
	}
	for _, body := range []string{"", "not json", `{"secret":""}`, `{"other":1}`} {
		if rr := f.req(http.MethodPost, "/api/login", body, ""); rr.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, rr.Code)
		}
	}
	big := `{"secret":"` + strings.Repeat("a", 5000) + `"}`
	if rr := f.req(http.MethodPost, "/api/login", big, ""); rr.Code != http.StatusBadRequest {
		t.Errorf("oversized body = %d, want 400", rr.Code)
	}
	// Surrounding whitespace (e.g. pasted with a newline) is tolerated.
	if rr := f.login(" " + goodSecret + " "); rr.Code != http.StatusOK {
		t.Errorf("secret with spaces = %d", rr.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	f := newAuthFixture(t)
	rr := f.req(http.MethodPost, "/api/logout", "", "tok-1")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rr.Code)
	}
	c := rr.Result().Cookies()
	if len(c) != 1 || c[0].Name != sessionCookieName || c[0].MaxAge >= 0 || c[0].Value != "" {
		t.Errorf("logout cookie = %+v", c)
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	for i := 0; i < 3; i++ {
		if rr := f.login("wrong"); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, rr.Code)
		}
	}
	// Blocked now, even with the right secret.
	rr := f.login(goodSecret)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("after 3 failures = %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") != "60" {
		t.Errorf("Retry-After = %q", rr.Header().Get("Retry-After"))
	}
	var body struct {
		RetryAfterSeconds int `json:"retryAfterSeconds"`
	}
	decode(t, rr, &body)
	if body.RetryAfterSeconds != 60 {
		t.Errorf("retryAfterSeconds = %d", body.RetryAfterSeconds)
	}

	f.clock.t = f.clock.t.Add(30 * time.Second)
	if rr := f.login(goodSecret); rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "30" {
		t.Errorf("mid-window = %d Retry-After=%s", rr.Code, rr.Header().Get("Retry-After"))
	}
	f.clock.t = f.clock.t.Add(31 * time.Second)
	if rr := f.login(goodSecret); rr.Code != http.StatusOK {
		t.Errorf("after window = %d", rr.Code)
	}
}

func TestSuccessfulLoginResetsFailures(t *testing.T) {
	f := newAuthFixture(t)
	f.login("wrong")
	f.login("wrong")
	if rr := f.login(goodSecret); rr.Code != http.StatusOK {
		t.Fatal(rr.Code)
	}
	f.login("wrong")
	f.login("wrong")
	if rr := f.login("wrong"); rr.Code != http.StatusUnauthorized {
		t.Errorf("counter should have been reset by the success, got %d", rr.Code)
	}
}

func TestRateLimitKeysByForwardedClientOnlyFromLoopback(t *testing.T) {
	f := newAuthFixture(t)
	// httptest requests come from 192.0.2.1 (not loopback): XFF is ignored,
	// so changing it does not escape the limit.
	for i := 0; i < 3; i++ {
		f.login("wrong", "X-Forwarded-For", "100.64.0."+string(rune('1'+i)))
	}
	if rr := f.login(goodSecret, "X-Forwarded-For", "100.64.0.9"); rr.Code != http.StatusTooManyRequests {
		t.Errorf("spoofed XFF from non-loopback must not bypass the limit: %d", rr.Code)
	}

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "100.89.18.49, 10.0.0.1")
	if got := clientKey(r); got != "100.89.18.49" {
		t.Errorf("from loopback proxy, key = %q, want first XFF entry", got)
	}
	r.Header.Del("X-Forwarded-For")
	if got := clientKey(r); got != "127.0.0.1" {
		t.Errorf("without XFF, key = %q", got)
	}
	r.RemoteAddr = "192.0.2.7:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientKey(r); got != "192.0.2.7" {
		t.Errorf("non-loopback peer, key = %q", got)
	}
}

func TestUnknownAPIPathIs404EvenWithWebClient(t *testing.T) {
	f := newAuthFixture(t)
	rr := f.req(http.MethodGet, "/api/nope", "", "tok-1")
	if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "<html>") {
		t.Errorf("/api/nope = %d %q, want JSON 404 (not the web client)", rr.Code, rr.Body.String())
	}
}

func TestAuthDisabledSessionAndLogin(t *testing.T) {
	h := newTest(t, newEngine(), "")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/session", ""},
		{http.MethodPost, "/api/login", `{"secret":"x"}`},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		var body sessionResponse
		decode(t, rr, &body)
		if rr.Code != http.StatusOK || !body.Authenticated || body.AuthRequired {
			t.Errorf("%s with auth disabled = %d %+v", tc.path, rr.Code, body)
		}
	}
}
