package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maikomr/watchmydog/internal/ports"
)

const (
	sessionCookieName = "wmd_session"
	maxLoginBody      = 4 << 10

	defaultLoginMaxFailures = 5
	defaultLoginWindow      = 15 * time.Minute
)

// --- session cookie -------------------------------------------------------

// sessionCookie builds the cookie that carries the session token.
//
//   - HttpOnly: page scripts cannot read it (XSS cannot steal it).
//   - Secure: only sent over HTTPS (tailscale serve) or to localhost.
//   - SameSite=Lax: not sent on cross-site POSTs, which blocks CSRF against
//     pause/resume/login while still allowing top-level links into the app.
func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

type principalKey struct{}

// principalFrom returns the authenticated caller stored by protect.
func principalFrom(ctx context.Context) (ports.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(ports.Principal)
	return p, ok
}

// authenticate checks the request's session cookie.
func (d Deps) authenticate(r *http.Request) (ports.Principal, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return ports.Principal{}, false
	}
	p, err := d.Auth.Verify(r.Context(), c.Value)
	return p, err == nil
}

// protect requires a valid session. With auth disabled it is a pass-through.
func (d Deps) protect(next http.Handler) http.Handler {
	if d.Auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := d.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// --- handlers -------------------------------------------------------------

type sessionResponse struct {
	Authenticated bool       `json:"authenticated"`
	AuthRequired  bool       `json:"authRequired"`
	Subject       string     `json:"subject,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
}

// handleSession tells the client whether it is logged in (200) or must
// show the login screen (401).
func (d Deps) handleSession(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: true})
		return
	}
	p, ok := d.authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{Authenticated: true, AuthRequired: true, Subject: p.Subject})
}

func (d Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: true})
		return
	}
	key := clientKey(r)
	if wait := d.limiter.retryAfter(key); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":             "too many failed attempts",
			"retryAfterSeconds": secs,
		})
		return
	}

	var body struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&body); err != nil || body.Secret == "" {
		writeError(w, http.StatusBadRequest, `body must be JSON {"secret": "..."}`)
		return
	}

	sess, err := d.Auth.Login(r.Context(), strings.TrimSpace(body.Secret))
	if err != nil {
		if errors.Is(err, ports.ErrInvalidCredentials) {
			d.limiter.fail(key)
			d.Logger.Warn("login failed", "client", key)
			writeError(w, http.StatusUnauthorized, "wrong secret")
			return
		}
		d.Logger.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	d.limiter.reset(key)
	d.Logger.Info("login", "client", key, "subject", sess.Principal.Subject)

	maxAge := int(sess.ExpiresAt.Sub(d.Now()).Seconds())
	http.SetCookie(w, sessionCookie(sess.Token, maxAge))
	exp := sess.ExpiresAt.UTC()
	writeJSON(w, http.StatusOK, sessionResponse{
		Authenticated: true, AuthRequired: true, Subject: sess.Principal.Subject, ExpiresAt: &exp,
	})
}

// handleLogout clears the cookie on this device. Sessions are stateless, so
// "log out everywhere" means rotating the secret (see ADR-0005).
func (d Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, sessionCookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

// --- rate limiting ----------------------------------------------------------

// loginLimiter blocks a client after max failed logins until a fixed window
// that started at its first failure has passed. A successful login clears
// the client's record.
type loginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	now     func() time.Time
	entries map[string]*limitEntry
}

type limitEntry struct {
	failures int
	resetAt  time.Time
}

func newLoginLimiter(max int, window time.Duration, now func() time.Time) *loginLimiter {
	return &loginLimiter{max: max, window: window, now: now, entries: map[string]*limitEntry{}}
}

// retryAfter returns how long key must wait before trying again (0 = may try).
func (l *loginLimiter) retryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return 0
	}
	now := l.now()
	if !now.Before(e.resetAt) {
		delete(l.entries, key)
		return 0
	}
	if e.failures >= l.max {
		return e.resetAt.Sub(now)
	}
	return 0
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entries[key]
	if e == nil || !now.Before(e.resetAt) {
		e = &limitEntry{resetAt: now.Add(l.window)}
		l.entries[key] = e
	}
	e.failures++
	// Keep the map bounded if many distinct clients fail.
	if len(l.entries) > 1024 {
		for k, v := range l.entries {
			if !now.Before(v.resetAt) {
				delete(l.entries, k)
			}
		}
	}
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// clientKey identifies the client for rate limiting. X-Forwarded-For is
// trusted only when the direct peer is loopback, i.e. a local reverse proxy
// such as `tailscale serve`; otherwise anyone could pick their own key.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	return host
}
