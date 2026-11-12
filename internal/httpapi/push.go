package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/maikomr/watchmydog/internal/app/alerts"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/ports"
)

// maxPushBody bounds subscription requests; a real one is about 500 bytes.
const maxPushBody = 8 << 10

// pushConfigView tells the client how to subscribe and what the rules are.
type pushConfigView struct {
	Enabled bool `json:"enabled"`
	// PublicKey is the applicationServerKey for PushManager.subscribe().
	PublicKey       string `json:"publicKey"`
	CooldownSeconds int    `json:"cooldownSeconds"`
	// QuietHours is null when none are configured. Times are the Mac's
	// local clock, "HH:MM".
	QuietHours *quietHoursView `json:"quietHours"`
}

type quietHoursView struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// subscriptionBody is what PushSubscription.toJSON() gives in the browser.
type subscriptionBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// handlePushConfig serves GET /api/push/config.
func (d Deps) handlePushConfig(w http.ResponseWriter, r *http.Request) {
	s := d.Alerts.Settings()
	v := pushConfigView{Enabled: s.Enabled, PublicKey: s.PublicKey, CooldownSeconds: int(s.Cooldown.Seconds())}
	if q := s.Quiet; q != nil {
		str := q.String() // "23:00-07:00"
		v.QuietHours = &quietHoursView{Start: str[:5], End: str[6:]}
	}
	writeJSON(w, http.StatusOK, v)
}

func (d Deps) readSubscription(w http.ResponseWriter, r *http.Request) (subscriptionBody, bool) {
	var body subscriptionBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPushBody)).Decode(&body); err != nil || body.Endpoint == "" {
		writeError(w, http.StatusBadRequest, `body must be JSON {"endpoint": "...", "keys": {...}}`)
		return body, false
	}
	return body, true
}

// handlePushSubscribe serves POST /api/push/subscribe. The app calls it
// when notifications are switched on and again on every start, so a device
// the server forgot (new database) is registered again.
func (d Deps) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	body, ok := d.readSubscription(w, r)
	if !ok {
		return
	}
	err := d.Alerts.Subscribe(r.Context(), domain.PushSubscription{
		Endpoint: body.Endpoint, P256DH: body.Keys.P256DH, Auth: body.Keys.Auth,
	})
	switch {
	case err == nil:
		d.Logger.Info("device subscribed to notifications")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, alerts.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, domain.ErrInvalidSubscription):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		d.Logger.Error("subscribe", "err", err)
		writeError(w, http.StatusInternalServerError, "could not store the subscription")
	}
}

// handlePushUnsubscribe serves POST /api/push/unsubscribe.
func (d Deps) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	body, ok := d.readSubscription(w, r)
	if !ok {
		return
	}
	if err := d.Alerts.Unsubscribe(r.Context(), body.Endpoint); err != nil {
		d.Logger.Error("unsubscribe", "err", err)
		writeError(w, http.StatusInternalServerError, "could not remove the subscription")
		return
	}
	d.Logger.Info("device unsubscribed from notifications")
	w.WriteHeader(http.StatusNoContent)
}

// handlePushTest serves POST /api/push/test: one test notification to the
// calling device.
func (d Deps) handlePushTest(w http.ResponseWriter, r *http.Request) {
	body, ok := d.readSubscription(w, r)
	if !ok {
		return
	}
	err := d.Alerts.SendTest(r.Context(), body.Endpoint)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, alerts.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, alerts.ErrUnknownDevice):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ports.ErrSubscriptionGone):
		writeError(w, http.StatusGone, "the browser's subscription has expired; switch notifications off and on again")
	default:
		// The push service (outside our network) refused or was unreachable.
		writeError(w, http.StatusBadGateway, err.Error())
	}
}
