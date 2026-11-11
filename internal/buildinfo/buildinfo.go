// Package buildinfo exposes version metadata injected at build time.
//
// The Makefile sets these with -ldflags "-X ...". When the binary is built
// with plain `go build` they keep their "dev" defaults.
package buildinfo

var (
	// Version is the semantic version or git describe output.
	Version = "dev"
	// Commit is the short git SHA the binary was built from.
	Commit = "unknown"
)
