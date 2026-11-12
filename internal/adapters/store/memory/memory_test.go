package memory

import (
	"testing"

	"github.com/maikomr/watchmydog/internal/adapters/store/storetest"
	"github.com/maikomr/watchmydog/internal/ports"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) ports.EventStore { return New() })
}
