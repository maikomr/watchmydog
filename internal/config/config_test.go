package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func noFile(string) ([]byte, error) { return nil, errors.New("no file") }

func fileWith(content string) func(string) ([]byte, error) {
	return func(string) ([]byte, error) { return []byte(content), nil }
}

const sampleYAML = `
listen: ":8080"
data_dir: data
go2rtc:
  bin: /usr/local/bin/go2rtc
  log_level: debug
cameras:
  - id: dog
    name: Living room
    video:
      device: REDRAGON Live Camera
      width: 1280
      height: 720
      fps: 30
      bitrate_kbps: 2500
      low:
        width: 640
        height: 360
    audio:
      device: REDRAGON Live Camera Audio
`

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, noEnv, noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q, want default", cfg.ListenAddr)
	}
	if cfg.Engine.Bin != "go2rtc" || cfg.Engine.WebRTCPort != 8555 {
		t.Errorf("engine defaults = %+v", cfg.Engine)
	}
	if len(cfg.Cameras) != 0 {
		t.Errorf("expected no cameras by default")
	}
}

func TestLoadYAMLFileAndRelativePaths(t *testing.T) {
	cfg, err := Load([]string{"-config", "/etc/wmd/watchmydog.yaml"}, noEnv, fileWith(sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.DataDir != "/etc/wmd/data" {
		t.Errorf("DataDir = %q, want resolved relative to config file", cfg.DataDir)
	}
	if cfg.WebDir != "/etc/wmd/web/dist" {
		t.Errorf("WebDir = %q, want default resolved relative to config file", cfg.WebDir)
	}
	if cfg.Engine.Bin != "/usr/local/bin/go2rtc" || cfg.Engine.LogLevel != "debug" {
		t.Errorf("engine = %+v", cfg.Engine)
	}
	if cfg.Engine.APIAddr != "127.0.0.1:1984" {
		t.Errorf("unset engine fields must keep defaults, got api=%q", cfg.Engine.APIAddr)
	}
	cams := cfg.DomainCameras()
	if len(cams) != 1 || cams[0].ID != "dog" || cams[0].Video.Width != 1280 || !cams[0].HasAudio() {
		t.Errorf("cameras = %+v", cams)
	}
	if cams[0].Video.Low == nil || cams[0].Video.Low.Width != 640 {
		t.Errorf("low variant not loaded: %+v", cams[0].Video)
	}
}

func TestConfigPathFromEnv(t *testing.T) {
	env := func(k string) string {
		if k == "WATCHMYDOG_CONFIG" {
			return "/x/cfg.yaml"
		}
		return ""
	}
	var asked string
	read := func(p string) ([]byte, error) { asked = p; return []byte(sampleYAML), nil }
	if _, err := Load(nil, env, read); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if asked != "/x/cfg.yaml" {
		t.Errorf("read %q, want path from env", asked)
	}
}

func TestPrecedenceFlagOverEnvOverFile(t *testing.T) {
	env := func(k string) string {
		if k == "WATCHMYDOG_LISTEN" {
			return "0.0.0.0:9000"
		}
		return ""
	}
	cfg, err := Load([]string{"-config", "c.yaml"}, env, fileWith(sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("env should beat file, got %q", cfg.ListenAddr)
	}
	cfg, err = Load([]string{"-config", "c.yaml", "-listen", ":1234"}, env, fileWith(sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":1234" {
		t.Errorf("flag should beat env, got %q", cfg.ListenAddr)
	}
}

func TestUnknownYAMLFieldIsAnError(t *testing.T) {
	_, err := Load([]string{"-config", "c.yaml"}, noEnv, fileWith("listen: ':1'\nbogus: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("expected unknown-field error, got %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	bad := `
cameras:
  - id: dog
    video: { device: cam, width: 1280, height: 720, fps: 30, bitrate_kbps: 2500 }
  - id: dog
    video: { device: "", width: 0, height: 0, fps: 0, bitrate_kbps: 0 }
`
	_, err := Load([]string{"-config", "c.yaml"}, noEnv, fileWith(bad))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"duplicate camera id", "video.device", "fps"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestMissingConfigFileIsAnError(t *testing.T) {
	if _, err := Load([]string{"-config", "missing.yaml"}, noEnv, noFile); err == nil {
		t.Error("expected error")
	}
}

func TestLoadRejectsUnknownFlag(t *testing.T) {
	if _, err := Load([]string{"-bogus"}, noEnv, noFile); err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestDeployExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile("../../deploy/watchmydog.example.yaml")
	if err != nil {
		t.Skip("example config not present:", err)
	}
	if _, err := Load([]string{"-config", "../../deploy/watchmydog.example.yaml"}, noEnv, func(string) ([]byte, error) { return data, nil }); err != nil {
		t.Errorf("deploy/watchmydog.example.yaml must load: %v", err)
	}
}
