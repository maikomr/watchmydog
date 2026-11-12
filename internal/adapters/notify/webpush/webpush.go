// Package webpush is a ports.Notifier that uses the Web Push standard: the
// service POSTs an encrypted message to the push service of the browser
// vendor (Google's for Chrome on Android), which wakes the phone and hands
// the message to our service worker.
//
//   - The message is encrypted to the device's keys (RFC 8291), so the push
//     service cannot read it.
//   - The request is signed with our VAPID key pair (RFC 8292), so only this
//     server can send to the subscriptions made with its public key.
//
// See ADR-0008.
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	lib "github.com/SherClockHolmes/webpush-go"

	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// Keys is the VAPID key pair, both base64url.
type Keys struct {
	// Public is given to browsers when they subscribe.
	Public string `json:"publicKey"`
	// Private signs every push request. Keep it secret.
	Private string `json:"privateKey"`
}

// LoadOrCreateKeys reads the key file, or creates it (mode 0600) with a new
// key pair when it does not exist. created reports which happened.
//
// The subscriptions of all devices are tied to the key pair: if the file is
// lost, every device has to switch notifications on again.
func LoadOrCreateKeys(path string) (keys Keys, created bool, err error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &keys); err != nil {
			return Keys{}, false, fmt.Errorf("vapid keys %s: %w", path, err)
		}
		if keys.Public == "" || keys.Private == "" {
			return Keys{}, false, fmt.Errorf("vapid keys %s: publicKey or privateKey is empty", path)
		}
		return keys, false, nil
	case !errors.Is(err, os.ErrNotExist):
		return Keys{}, false, fmt.Errorf("vapid keys: %w", err)
	}

	keys.Private, keys.Public, err = lib.GenerateVAPIDKeys()
	if err != nil {
		return Keys{}, false, fmt.Errorf("generate vapid keys: %w", err)
	}
	data, err = json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return Keys{}, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Keys{}, false, fmt.Errorf("vapid keys: %w", err)
	}
	// O_EXCL: never overwrite a key file that appeared in the meantime.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Keys{}, false, fmt.Errorf("vapid keys: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return Keys{}, false, fmt.Errorf("vapid keys: %w", err)
	}
	if err := f.Close(); err != nil {
		return Keys{}, false, fmt.Errorf("vapid keys: %w", err)
	}
	return keys, true, nil
}

// Settings configures the notifier.
type Settings struct {
	Keys Keys
	// Contact is an email address or https URL sent to the push service
	// with every request, so its operator can reach the sender (required
	// by RFC 8292).
	Contact string
	// TTL is how long the push service keeps a message for a device that
	// is offline. Default one hour.
	TTL time.Duration
	// HTTPClient is used for requests; nil means a client with a 15 s timeout.
	HTTPClient *http.Client
}

// Notifier implements ports.Notifier.
type Notifier struct {
	s Settings
}

var _ ports.Notifier = (*Notifier)(nil)

// New builds the notifier.
func New(s Settings) (*Notifier, error) {
	if s.Keys.Public == "" || s.Keys.Private == "" {
		return nil, errors.New("webpush: VAPID keys are required")
	}
	if s.Contact == "" {
		return nil, errors.New("webpush: contact is required")
	}
	// The library adds "mailto:" itself unless it sees an https URL.
	s.Contact = strings.TrimPrefix(s.Contact, "mailto:")
	if s.TTL <= 0 {
		s.TTL = time.Hour
	}
	if s.HTTPClient == nil {
		s.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Notifier{s: s}, nil
}

// PublicKey is the key browsers subscribe with ("applicationServerKey").
func (n *Notifier) PublicKey() string { return n.s.Keys.Public }

// Payload is the JSON the service worker receives (web/src/sw/notify.ts
// reads the same shape).
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag,omitempty"`
	// TS is the time of the reported thing in Unix milliseconds.
	TS int64 `json:"ts"`
}

// Notify implements ports.Notifier.
func (n *Notifier) Notify(ctx context.Context, sub domain.PushSubscription, msg domain.Notification) error {
	body, err := json.Marshal(Payload{Title: msg.Title, Body: msg.Body, URL: msg.URL, Tag: msg.Tag, TS: msg.At.UnixMilli()})
	if err != nil {
		return err
	}
	res, err := lib.SendNotificationWithContext(ctx, body, &lib.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     lib.Keys{P256dh: sub.P256DH, Auth: sub.Auth},
	}, &lib.Options{
		HTTPClient:      n.s.HTTPClient,
		Subscriber:      n.s.Contact,
		VAPIDPublicKey:  n.s.Keys.Public,
		VAPIDPrivateKey: n.s.Keys.Private,
		TTL:             int(n.s.TTL.Seconds()),
		// High: deliver now even if the phone is in battery-saving doze.
		Urgency: lib.UrgencyHigh,
	})
	if err != nil {
		return fmt.Errorf("webpush: %w", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ports.ErrSubscriptionGone
	default:
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("webpush: push service answered %s: %s", res.Status, strings.TrimSpace(string(detail)))
	}
}
