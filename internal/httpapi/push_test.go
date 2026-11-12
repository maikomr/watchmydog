package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/app/alerts"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// recNotifier records notifications; err, when set, is returned instead.
type recNotifier struct {
	mu   sync.Mutex
	sent []domain.Notification
	err  error
}

func (n *recNotifier) Notify(_ context.Context, _ domain.PushSubscription, msg domain.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return n.err
	}
	n.sent = append(n.sent, msg)
	return nil
}

func newAlerts(store ports.SubscriptionStore, n ports.Notifier, s alerts.Settings) *alerts.Service {
	return alerts.New(store, n, []domain.Camera{dog()}, s, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func pushHandler(a *alerts.Service) http.Handler {
	return NewHandler(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Cameras: []domain.Camera{dog()}, Alerts: a})
}

func subscriptionJSON(endpoint string) string {
	b, _ := json.Marshal(map[string]any{
		"endpoint":       endpoint,
		"expirationTime": nil, // browsers send this; it must be tolerated
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(make([]byte, 65)),
			"auth":   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		},
	})
	return string(b)
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	return rr
}

const phone = "https://push.example/send/phone"

func TestPushConfig(t *testing.T) {
	quiet := domain.DailyWindow{StartMin: 23 * 60, EndMin: 7*60 + 5}
	h := pushHandler(newAlerts(memory.New(), &recNotifier{}, alerts.Settings{
		Enabled: true, PublicKey: "BPublicKey", Cooldown: 5 * time.Minute, Quiet: &quiet,
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/push/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	want := `{"enabled":true,"publicKey":"BPublicKey","cooldownSeconds":300,"quietHours":{"start":"23:00","end":"07:05"}}`
	if got := strings.TrimSpace(rr.Body.String()); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}

	h = pushHandler(newAlerts(memory.New(), nil, alerts.Settings{}))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/push/config", nil))
	want = `{"enabled":false,"publicKey":"","cooldownSeconds":0,"quietHours":null}`
	if got := strings.TrimSpace(rr.Body.String()); got != want {
		t.Errorf("disabled body = %s", got)
	}
}

func TestPushSubscribeTestUnsubscribe(t *testing.T) {
	store, notif := memory.New(), &recNotifier{}
	h := pushHandler(newAlerts(store, notif, alerts.Settings{Enabled: true, PublicKey: "k"}))
	ctx := context.Background()

	// Test before subscribing: the server does not know the device.
	if rr := post(h, "/api/push/test", subscriptionJSON(phone)); rr.Code != http.StatusNotFound {
		t.Errorf("test before subscribe = %d, want 404", rr.Code)
	}

	for i := 0; i < 2; i++ { // subscribing twice is fine (the app does it on every start)
		if rr := post(h, "/api/push/subscribe", subscriptionJSON(phone)); rr.Code != http.StatusNoContent {
			t.Fatalf("subscribe = %d %s", rr.Code, rr.Body)
		}
	}
	subs, _ := store.ListSubscriptions(ctx)
	if len(subs) != 1 || subs[0].Endpoint != phone {
		t.Fatalf("stored = %+v", subs)
	}

	if rr := post(h, "/api/push/test", subscriptionJSON(phone)); rr.Code != http.StatusNoContent {
		t.Fatalf("test = %d %s", rr.Code, rr.Body)
	}
	if len(notif.sent) != 1 || notif.sent[0].Title != "watchmydog test" {
		t.Errorf("sent = %+v", notif.sent)
	}

	notif.err = errors.New("webpush: push service answered 403 Forbidden")
	if rr := post(h, "/api/push/test", subscriptionJSON(phone)); rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "403") {
		t.Errorf("failing push service = %d %s, want 502 with the reason", rr.Code, rr.Body)
	}
	notif.err = ports.ErrSubscriptionGone
	if rr := post(h, "/api/push/test", subscriptionJSON(phone)); rr.Code != http.StatusGone {
		t.Errorf("gone subscription = %d, want 410", rr.Code)
	}
	notif.err = nil

	// Gone devices are forgotten; subscribe again to test unsubscribe.
	post(h, "/api/push/subscribe", subscriptionJSON(phone))
	if rr := post(h, "/api/push/unsubscribe", `{"endpoint":"`+phone+`"}`); rr.Code != http.StatusNoContent {
		t.Fatalf("unsubscribe = %d %s", rr.Code, rr.Body)
	}
	if subs, _ := store.ListSubscriptions(ctx); len(subs) != 0 {
		t.Errorf("after unsubscribe: %+v", subs)
	}
	if rr := post(h, "/api/push/unsubscribe", `{"endpoint":"`+phone+`"}`); rr.Code != http.StatusNoContent {
		t.Errorf("second unsubscribe = %d, want 204", rr.Code)
	}
}

func TestPushBadRequests(t *testing.T) {
	store := memory.New()
	h := pushHandler(newAlerts(store, &recNotifier{}, alerts.Settings{Enabled: true}))
	for name, body := range map[string]string{
		"not json":      "hello",
		"no endpoint":   `{"keys":{}}`,
		"http endpoint": strings.Replace(subscriptionJSON(phone), "https://", "http://", 1),
		"bad keys":      `{"endpoint":"` + phone + `","keys":{"p256dh":"AAAA","auth":"AAAA"}}`,
		"huge":          `{"endpoint":"https://push.example/` + strings.Repeat("a", 9000) + `"}`,
	} {
		if rr := post(h, "/api/push/subscribe", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rr.Code)
		}
	}
	if subs, _ := store.ListSubscriptions(context.Background()); len(subs) != 0 {
		t.Errorf("bad requests stored something: %+v", subs)
	}

	store.FailWith = errors.New("disk full")
	rr := post(h, "/api/push/subscribe", subscriptionJSON(phone))
	if rr.Code != http.StatusInternalServerError || strings.Contains(rr.Body.String(), "disk full") {
		t.Errorf("store failure = %d %s, want 500 without internals", rr.Code, rr.Body)
	}
}

func TestPushDisabledAndAbsent(t *testing.T) {
	h := pushHandler(newAlerts(memory.New(), nil, alerts.Settings{}))
	for _, path := range []string{"/api/push/subscribe", "/api/push/test"} {
		if rr := post(h, path, subscriptionJSON(phone)); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s when disabled = %d, want 503", path, rr.Code)
		}
	}
	// Without the service the routes do not exist.
	h = NewHandler(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if rr := post(h, "/api/push/subscribe", subscriptionJSON(phone)); rr.Code != http.StatusNotFound {
		t.Errorf("without alerts = %d, want 404", rr.Code)
	}
}
