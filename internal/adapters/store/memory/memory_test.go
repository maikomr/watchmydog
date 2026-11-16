package memory

import (
	"testing"

	"github.com/maikomr/watchmydog/internal/adapters/store/storetest"
	"github.com/maikomr/watchmydog/internal/ports"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) ports.EventStore { return New() })
}

func TestClipContract(t *testing.T) {
	storetest.RunClips(t, func(*testing.T) ports.ClipStore { return New() })
}

func TestPauseContract(t *testing.T) {
	storetest.RunPauses(t, func(*testing.T) ports.PauseStore { return New() })
}

func TestSubscriptionContract(t *testing.T) {
	storetest.RunSubscriptions(t, func(*testing.T) ports.SubscriptionStore { return New() })
}
