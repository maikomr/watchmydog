// Package power keeps the host awake while the service runs.
//
// A sleeping Mac is offline for Tailscale, so viewers cannot connect, and
// "Wake for network access" does not wake it for tailnet traffic. Relying
// on the Energy settings alone is fragile (a macOS update or a settings
// reset can change them), so the service holds its own power assertion,
// the same mechanism `caffeinate -i` uses. The kernel releases it
// automatically if the process dies.
//
// Only idle system sleep is prevented: the display still sleeps, and the
// user can still put the Mac to sleep from the Apple menu.
package power

// Assertion is a held "prevent idle system sleep" request.
type Assertion interface {
	// Release lets the system sleep again. Safe to call more than once.
	Release() error
}

// Supported reports whether this platform implements sleep prevention.
func Supported() bool { return supported }
