// Package alerts is the application service that turns events into
// notifications. It decides whether an event is worth a notification
// (cooldown, quiet hours), sends it to every subscribed device through a
// ports.Notifier and forgets devices whose subscription is gone.
//
// It does no I/O of its own, so it is tested with a fake notifier and the
// in-memory store. See ADR-0008.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Settings are the notification rules.
type Settings struct {
	// Enabled is false when notifications are switched off in the config.
	Enabled bool
	// PublicKey is what browsers need to subscribe (the VAPID public key).
	PublicKey string
	// Cooldown is the minimum time between two notifications for the same
	// camera. Zero means every event notifies.
	Cooldown time.Duration
	// Quiet is a daily time span without notifications; nil means none.
	Quiet *domain.DailyWindow
}

// ErrDisabled is returned when notifications are switched off.
var ErrDisabled = errors.New("notifications are switched off in the config")

// ErrUnknownDevice is returned for an endpoint that is not subscribed.
var ErrUnknownDevice = errors.New("this device is not subscribed")

const (
	// queueSize is how many events may wait to be sent. Sending takes a
	// second or two and events are at least seconds apart, so the queue is
	// normally empty.
	queueSize = 16
	// sendTimeout bounds one round of sending to all devices.
	sendTimeout = 20 * time.Second
)

// Service decides about and sends notifications.
type Service struct {
	store    ports.SubscriptionStore
	notifier ports.Notifier
	settings Settings
	names    map[domain.CameraID]string
	logger   *slog.Logger
	now      func() time.Time
	queue    chan domain.Event

	mu       sync.Mutex
	lastSent map[domain.CameraID]time.Time
}

// New builds the service. notifier may be nil when s.Enabled is false.
// Events are only sent while Run is running.
func New(store ports.SubscriptionStore, notifier ports.Notifier, cameras []domain.Camera, s Settings, logger *slog.Logger) *Service {
	names := map[domain.CameraID]string{}
	for _, c := range cameras {
		names[c.ID] = c.Name
	}
	if notifier == nil {
		s.Enabled = false
	}
	return &Service{
		store: store, notifier: notifier, settings: s, names: names, logger: logger,
		now: time.Now, queue: make(chan domain.Event, queueSize),
		lastSent: map[domain.CameraID]time.Time{},
	}
}

// Settings returns the notification rules.
func (s *Service) Settings() Settings { return s.settings }

// EventStarted hands a new event to the service. It never blocks: the
// sound detector calls it and must not wait for the network.
func (s *Service) EventStarted(e domain.Event) {
	if !s.settings.Enabled {
		return
	}
	select {
	case s.queue <- e:
	default:
		s.logger.Warn("notification queue is full; not notifying", "event", e.ID)
	}
}

// Run sends notifications for queued events until ctx is done.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.queue:
			s.handle(ctx, e)
		}
	}
}

func (s *Service) handle(ctx context.Context, e domain.Event) {
	now := s.now()
	if reason := s.suppress(e, now); reason != "" {
		s.logger.Info("no notification", "event", e.ID, "camera", e.CameraID, "reason", reason)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	sent, total, err := s.broadcast(ctx, s.eventNotification(e))
	if err != nil {
		s.logger.Error("could not read subscribed devices", "event", e.ID, "err", err)
		return
	}
	if total == 0 {
		s.logger.Info("no notification", "event", e.ID, "camera", e.CameraID, "reason", "no device has notifications switched on")
		return
	}
	if sent > 0 {
		// Only a notification that went out starts the cooldown: if nothing
		// could be delivered, the next event should try again.
		s.mu.Lock()
		s.lastSent[e.CameraID] = now
		s.mu.Unlock()
	}
	s.logger.Info("notification sent", "event", e.ID, "camera", e.CameraID, "devices", total, "delivered", sent)
}

// suppress returns why e gets no notification, or "" when it gets one.
func (s *Service) suppress(e domain.Event, now time.Time) string {
	if q := s.settings.Quiet; q != nil && q.Contains(now) {
		return "quiet hours " + q.String()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSent[e.CameraID]; ok && s.settings.Cooldown > 0 {
		if wait := s.settings.Cooldown - now.Sub(last); wait > 0 {
			return fmt.Sprintf("cooldown, %s left", wait.Round(time.Second))
		}
	}
	return ""
}

func (s *Service) cameraName(id domain.CameraID) string {
	if n := s.names[id]; n != "" {
		return n
	}
	return string(id)
}

func (s *Service) eventNotification(e domain.Event) domain.Notification {
	return domain.Notification{
		Title: "Noise: " + s.cameraName(e.CameraID),
		Body:  "Tap to watch live.",
		URL:   "/?camera=" + url.QueryEscape(string(e.CameraID)),
		// One tag per camera: a newer notification replaces the older one.
		Tag: "sound-" + string(e.CameraID),
		At:  e.StartedAt,
	}
}

// broadcast sends n to every device in parallel and drops the ones that
// are gone. It returns how many deliveries were accepted.
func (s *Service) broadcast(ctx context.Context, n domain.Notification) (sent, total int, err error) {
	subs, err := s.store.ListSubscriptions(ctx)
	if err != nil {
		return 0, 0, err
	}
	results := make([]error, len(subs))
	var wg sync.WaitGroup
	for i, sub := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.send(ctx, sub, n)
		}()
	}
	wg.Wait()
	for _, err := range results {
		if err == nil {
			sent++
		}
	}
	return sent, len(subs), nil
}

// send delivers to one device and forgets it when its subscription is gone.
func (s *Service) send(ctx context.Context, sub domain.PushSubscription, n domain.Notification) error {
	err := s.notifier.Notify(ctx, sub, n)
	switch {
	case err == nil:
	case errors.Is(err, ports.ErrSubscriptionGone):
		s.logger.Info("device unsubscribed; forgetting it", "device", deviceLabel(sub.Endpoint))
		// Not the request's context: clean up even if it was just cancelled.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if derr := s.store.DeleteSubscription(cleanup, sub.Endpoint); derr != nil {
			s.logger.Error("could not forget device", "err", derr)
		}
	default:
		s.logger.Error("notification not delivered", "device", deviceLabel(sub.Endpoint), "err", err)
	}
	return err
}

// Subscribe registers a device, or refreshes its keys.
func (s *Service) Subscribe(ctx context.Context, sub domain.PushSubscription) error {
	if !s.settings.Enabled {
		return ErrDisabled
	}
	if err := sub.Validate(); err != nil {
		return err
	}
	sub.CreatedAt = s.now().UTC()
	return s.store.SaveSubscription(ctx, sub)
}

// Unsubscribe forgets a device. Unknown endpoints are fine.
func (s *Service) Unsubscribe(ctx context.Context, endpoint string) error {
	return s.store.DeleteSubscription(ctx, endpoint)
}

// SendTest sends a test notification to one subscribed device, ignoring
// cooldown and quiet hours, and returns the delivery error if any.
func (s *Service) SendTest(ctx context.Context, endpoint string) error {
	if !s.settings.Enabled {
		return ErrDisabled
	}
	subs, err := s.store.ListSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if sub.Endpoint == endpoint {
			return s.send(ctx, sub, domain.Notification{
				Title: "watchmydog test",
				Body:  "Notifications work on this device.",
				URL:   "/",
				Tag:   "test",
				At:    s.now(),
			})
		}
	}
	return ErrUnknownDevice
}

// deviceLabel shortens an endpoint for logs: the push service's host and
// the last characters of the URL. The full URL is a capability (whoever has
// it and our key can send), so it is not logged.
func deviceLabel(endpoint string) string {
	host := "?"
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	tail := endpoint
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	return host + "/…" + tail
}
