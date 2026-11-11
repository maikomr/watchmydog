//go:build !darwin

package power

const supported = false

type noopAssertion struct{}

func (noopAssertion) Release() error { return nil }

// PreventIdleSleep is a no-op on platforms without an implementation
// (a Linux edge node would use systemd-inhibit here).
func PreventIdleSleep(string) (Assertion, error) { return noopAssertion{}, nil }
