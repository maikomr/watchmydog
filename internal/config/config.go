// Package config loads runtime configuration for the watchmydog service.
//
// Increment 0 only needs the listen address. Later increments add a YAML
// file for cameras, go2rtc, detection thresholds and so on; the precedence
// will stay: defaults < YAML file < environment variables < command-line flags.
package config

import (
	"flag"
	"fmt"
	"io"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// ListenAddr is the host:port the HTTP API binds to.
	ListenAddr string
}

// Defaults returns the configuration used when nothing is overridden.
func Defaults() Config {
	return Config{ListenAddr: "127.0.0.1:8080"}
}

// Load resolves configuration from environment variables and command-line
// arguments. getenv is injected so tests do not touch the real environment.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Defaults()

	if v := getenv("WATCHMYDOG_LISTEN"); v != "" {
		cfg.ListenAddr = v
	}

	fs := flag.NewFlagSet("watchmydog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "address to listen on (host:port)")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("listen address must not be empty")
	}
	return cfg, nil
}
