package alerts

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/store/memory"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

type sent struct {
	endpoint string
	n        domain.Notification
}

// fakeNotifier records what was sent and fails for chosen endpoints.
type fakeNotifier struct {
	mu   sync.Mutex
	sent []sent
	fail map[string]error
}

func (f *fakeNotifier) Notify(_ context.Context, sub domain.PushSubscription, n domain.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[sub.Endpoint]; err != nil {
		return err
	}
	f.sent = append(f.sent, sent{sub.Endpoint, n})
	return nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func sub(name string) domain.PushSubscription {
	return domain.PushSubscription{
		Endpoint: "https://push.example/" + name,
		P256DH:   base64.RawURLEncoding.EncodeToString(make([]byte, 65)),
		Auth:     base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	}
}

type fixture struct {
	svc   *Service
	store *memory.Store
	notif *fakeNotifier
	now   time.Time
}

var t0 = time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC)

func newFixture(t *testing.T, s Settings, devices ...string) *fixture {
	t.Helper()
	f := &fixture{store: memory.New(), notif: &fakeNotifier{fail: map[string]error{}}, now: t0}
	cams := []domain.Camera{{ID: "dog", Name: "Living room"}, {ID: "yard"}}
	f.svc = New(f.store, f.notif, cams, s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.svc.now = func() time.Time { return f.now }
	for _, d := range devices {
		if err := f.svc.Subscribe(context.Background(), sub(d)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func event(id string, cam domain.CameraID) domain.Event {
	return domain.Event{ID: domain.EventID(id), CameraID: cam, Kind: domain.EventSound, StartedAt: t0, PeakDBFS: -20}
}

func (f *fixture) endpoints(t *testing.T) []string {
	t.Helper()
	subs, err := f.store.ListSubscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, s := range subs {
		out = append(out, s.Endpoint)
	}
	return out
}

func TestEventNotifiesEveryDevice(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true}, "phone", "tablet")
	f.svc.handle(context.Background(), event("e1", "dog"))

	if len(f.notif.sent) != 2 {
		t.Fatalf("sent %d notifications, want 2", len(f.notif.sent))
	}
	n := f.notif.sent[0].n
	if n.Title != "Noise: Living room" || n.URL != "/?camera=dog" || n.Tag != "sound-dog" || !n.At.Equal(t0) {
		t.Errorf("notification = %+v", n)
	}

	// A camera without a name is called by its ID.
	f.svc.handle(context.Background(), event("e2", "yard"))
	if got := f.notif.sent[len(f.notif.sent)-1].n.Title; got != "Noise: yard" {
		t.Errorf("title = %q", got)
	}
}

func TestCooldownIsPerCamera(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true, Cooldown: 5 * time.Minute}, "phone")
	ctx := context.Background()

	f.svc.handle(ctx, event("e1", "dog"))
	f.now = t0.Add(4*time.Minute + 59*time.Second)
	f.svc.handle(ctx, event("e2", "dog")) // too soon
	if got := f.notif.count(); got != 1 {
		t.Fatalf("after an event inside the cooldown: %d notifications, want 1", got)
	}
	f.svc.handle(ctx, event("e3", "yard")) // other camera is not affected
	if got := f.notif.count(); got != 2 {
		t.Fatalf("other camera: %d notifications, want 2", got)
	}
	f.now = t0.Add(5 * time.Minute)
	f.svc.handle(ctx, event("e4", "dog"))
	if got := f.notif.count(); got != 3 {
		t.Fatalf("after the cooldown: %d notifications, want 3", got)
	}
	// The suppressed event did not restart the cooldown; e4 did.
	f.now = t0.Add(9 * time.Minute)
	f.svc.handle(ctx, event("e5", "dog"))
	if got := f.notif.count(); got != 3 {
		t.Fatalf("4 min after e4: %d notifications, want 3", got)
	}
}

func TestCooldownStartsOnlyWhenSomethingWasDelivered(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true, Cooldown: 5 * time.Minute})
	ctx := context.Background()

	f.svc.handle(ctx, event("e1", "dog")) // nobody is subscribed yet
	if err := f.svc.Subscribe(ctx, sub("phone")); err != nil {
		t.Fatal(err)
	}
	f.notif.fail[sub("phone").Endpoint] = errors.New("no internet")
	f.now = t0.Add(10 * time.Second)
	f.svc.handle(ctx, event("e2", "dog")) // delivery fails
	delete(f.notif.fail, sub("phone").Endpoint)

	f.now = t0.Add(20 * time.Second)
	f.svc.handle(ctx, event("e3", "dog"))
	if got := f.notif.count(); got != 1 {
		t.Fatalf("first deliverable event: %d notifications, want 1", got)
	}
	f.now = t0.Add(30 * time.Second)
	f.svc.handle(ctx, event("e4", "dog"))
	if got := f.notif.count(); got != 1 {
		t.Fatalf("event inside the cooldown: %d notifications, want 1", got)
	}
}

func TestQuietHoursSuppressWithoutStartingTheCooldown(t *testing.T) {
	quiet := domain.DailyWindow{StartMin: 23 * 60, EndMin: 7 * 60}
	f := newFixture(t, Settings{Enabled: true, Cooldown: time.Hour, Quiet: &quiet}, "phone")
	ctx := context.Background()

	f.now = time.Date(2026, 11, 12, 6, 59, 0, 0, time.UTC)
	f.svc.handle(ctx, event("night", "dog"))
	if got := f.notif.count(); got != 0 {
		t.Fatalf("during quiet hours: %d notifications, want 0", got)
	}
	f.now = time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	f.svc.handle(ctx, event("morning", "dog"))
	if got := f.notif.count(); got != 1 {
		t.Fatalf("right after quiet hours: %d notifications, want 1", got)
	}
}

func TestGoneDevicesAreForgottenOthersKept(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true}, "gone", "flaky", "fine")
	f.notif.fail[sub("gone").Endpoint] = ports.ErrSubscriptionGone
	f.notif.fail[sub("flaky").Endpoint] = errors.New("push service answered 500")

	f.svc.handle(context.Background(), event("e1", "dog"))

	if got := f.notif.count(); got != 1 {
		t.Errorf("delivered %d, want 1", got)
	}
	got := f.endpoints(t)
	if len(got) != 2 || got[0] != sub("flaky").Endpoint || got[1] != sub("fine").Endpoint {
		t.Errorf("remaining devices = %v, want flaky and fine", got)
	}
}

func TestRunSendsQueuedEventsAndStops(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true}, "phone")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()

	f.svc.EventStarted(event("e1", "dog"))
	deadline := time.Now().Add(2 * time.Second)
	for f.notif.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no notification within 2 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestEventStartedNeverBlocks(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true}, "phone")
	done := make(chan struct{})
	go func() {
		for i := 0; i < queueSize*3; i++ { // nobody is running Run
			f.svc.EventStarted(event("e", "dog"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("EventStarted blocked on a full queue")
	}
}

func TestDisabled(t *testing.T) {
	ctx := context.Background()
	for name, f := range map[string]*fixture{
		"switched off": newFixture(t, Settings{Enabled: false}),
		"no notifier": func() *fixture {
			f := &fixture{store: memory.New()}
			f.svc = New(f.store, nil, nil, Settings{Enabled: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			return f
		}(),
	} {
		if f.svc.Settings().Enabled {
			t.Errorf("%s: Settings().Enabled is true", name)
		}
		if err := f.svc.Subscribe(ctx, sub("phone")); !errors.Is(err, ErrDisabled) {
			t.Errorf("%s: Subscribe err = %v, want ErrDisabled", name, err)
		}
		if err := f.svc.SendTest(ctx, sub("phone").Endpoint); !errors.Is(err, ErrDisabled) {
			t.Errorf("%s: SendTest err = %v, want ErrDisabled", name, err)
		}
		f.svc.EventStarted(event("e1", "dog")) // must not queue or panic
		if len(f.svc.queue) != 0 {
			t.Errorf("%s: event was queued", name)
		}
	}
}

func TestSubscribeValidatesAndUnsubscribeForgets(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true})
	ctx := context.Background()

	bad := sub("phone")
	bad.Endpoint = "http://not-https.example/x"
	if err := f.svc.Subscribe(ctx, bad); err == nil {
		t.Error("accepted a non-https endpoint")
	}
	if err := f.svc.Subscribe(ctx, sub("phone")); err != nil {
		t.Fatal(err)
	}
	subs, _ := f.store.ListSubscriptions(ctx)
	if len(subs) != 1 || !subs[0].CreatedAt.Equal(t0) {
		t.Fatalf("stored = %+v", subs)
	}
	if err := f.svc.Unsubscribe(ctx, sub("phone").Endpoint); err != nil {
		t.Fatal(err)
	}
	if got := f.endpoints(t); len(got) != 0 {
		t.Errorf("after unsubscribe: %v", got)
	}
}

func TestSendTest(t *testing.T) {
	quiet := domain.DailyWindow{StartMin: 0, EndMin: 23*60 + 59} // nearly always quiet
	f := newFixture(t, Settings{Enabled: true, Cooldown: time.Hour, Quiet: &quiet}, "phone", "tablet")
	ctx := context.Background()

	if err := f.svc.SendTest(ctx, sub("phone").Endpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SendTest(ctx, sub("phone").Endpoint); err != nil { // no cooldown for tests
		t.Fatal(err)
	}
	if len(f.notif.sent) != 2 || f.notif.sent[0].endpoint != sub("phone").Endpoint {
		t.Fatalf("sent = %+v, want two to the phone only", f.notif.sent)
	}
	if err := f.svc.SendTest(ctx, "https://push.example/stranger"); !errors.Is(err, ErrUnknownDevice) {
		t.Errorf("unknown device: err = %v", err)
	}

	f.notif.fail[sub("tablet").Endpoint] = ports.ErrSubscriptionGone
	if err := f.svc.SendTest(ctx, sub("tablet").Endpoint); !errors.Is(err, ports.ErrSubscriptionGone) {
		t.Errorf("gone device: err = %v", err)
	}
	if got := f.endpoints(t); len(got) != 1 {
		t.Errorf("gone device not forgotten: %v", got)
	}
}

func TestStoreFailureIsNotFatal(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true}, "phone")
	f.store.FailWith = errors.New("disk full")
	f.svc.handle(context.Background(), event("e1", "dog")) // logs, does not panic
	if got := f.notif.count(); got != 0 {
		t.Errorf("sent %d", got)
	}
}

func TestDeviceLabelHidesTheEndpoint(t *testing.T) {
	got := deviceLabel("https://fcm.googleapis.com/fcm/send/SECRETSECRETSECRET-abc123")
	if got != "fcm.googleapis.com/…abc123" {
		t.Errorf("label = %q", got)
	}
}

func TestHoldOffSuppressesWithoutStartingTheCooldown(t *testing.T) {
	f := newFixture(t, Settings{Enabled: true, Cooldown: 5 * time.Minute}, "phone")
	ctx := context.Background()

	f.svc.HoldOff("dog", t0.Add(10*time.Second))
	f.svc.HoldOff("dog", t0.Add(5*time.Second)) // an earlier end does not shorten it
	f.now = t0.Add(9 * time.Second)
	f.svc.handle(ctx, event("e1", "dog"))
	if got := f.notif.count(); got != 0 {
		t.Fatalf("while held off: %d notifications, want 0", got)
	}
	f.svc.handle(ctx, event("e2", "yard")) // other camera is not affected
	if got := f.notif.count(); got != 1 {
		t.Fatalf("other camera: %d notifications, want 1", got)
	}
	f.now = t0.Add(10 * time.Second)
	f.svc.handle(ctx, event("e3", "dog"))
	if got := f.notif.count(); got != 2 {
		t.Fatalf("after the hold: %d notifications, want 2 (no cooldown from the suppressed event)", got)
	}
}
