package config

import "testing"

func noEnv(string) string { return "" }

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q, want default", cfg.ListenAddr)
	}
}

func TestLoadEnvOverridesDefault(t *testing.T) {
	env := func(k string) string {
		if k == "WATCHMYDOG_LISTEN" {
			return "0.0.0.0:9000"
		}
		return ""
	}
	cfg, err := Load(nil, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("ListenAddr = %q, want env value", cfg.ListenAddr)
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	env := func(string) string { return "0.0.0.0:9000" }
	cfg, err := Load([]string{"-listen", ":1234"}, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":1234" {
		t.Errorf("ListenAddr = %q, want flag value", cfg.ListenAddr)
	}
}

func TestLoadRejectsEmptyListen(t *testing.T) {
	if _, err := Load([]string{"-listen", ""}, noEnv); err == nil {
		t.Error("expected error for empty listen address")
	}
}

func TestLoadRejectsUnknownFlag(t *testing.T) {
	if _, err := Load([]string{"-bogus"}, noEnv); err == nil {
		t.Error("expected error for unknown flag")
	}
}
