package ports

import (
	"context"
	"errors"

	"github.com/maikomr/watchmydog/internal/domain"
)

// Notifier delivers a notification to one subscribed device.
//
// The current implementation is Web Push with VAPID (works with any browser
// vendor's push service, no account needed). A cloud deployment with native
// apps would add FCM/APNs behind the same interface.
type Notifier interface {
	// Notify sends n to the device behind sub. It returns
	// ErrSubscriptionGone when the device has unsubscribed for good, so the
	// caller can forget the subscription.
	Notify(ctx context.Context, sub domain.PushSubscription, n domain.Notification) error
}

// ErrSubscriptionGone means the push service no longer knows the
// subscription (app uninstalled, permission revoked, or it expired).
var ErrSubscriptionGone = errors.New("push subscription is gone")

// SubscriptionStore keeps the devices that want notifications.
type SubscriptionStore interface {
	// SaveSubscription inserts the subscription, or replaces the keys of
	// the one with the same endpoint (its CreatedAt is kept).
	SaveSubscription(ctx context.Context, sub domain.PushSubscription) error
	// DeleteSubscription removes it; an unknown endpoint is not an error.
	DeleteSubscription(ctx context.Context, endpoint string) error
	// ListSubscriptions returns all subscriptions, oldest first.
	ListSubscriptions(ctx context.Context) ([]domain.PushSubscription, error)
}
