package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const testSecret = "test-secret-123456"

// env returns a getenv func backed by a map.
func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// envSecret is the minimal environment for a valid config.
var envSecret = env(map[string]string{"WATCHMYDOG_SECRET": testSecret})

func noFile(string) ([]byte, error) { return nil, errors.New("no file") }

// files returns a readFile func backed by a map of path -> content.
func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if c, ok := m[p]; ok {
			return []byte(c), nil
		}
		return nil, os.ErrNotExist
	}
}

const sampleYAML = `
listen: ":8080"
data_dir: data
auth:
  secret_file: secret
  session_ttl: 48h
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

func sampleFiles() func(string) ([]byte, error) {
	return files(map[string]string{
		"/etc/wmd/watchmydog.yaml": sampleYAML,
		"/etc/wmd/secret":          testSecret + "\n",
	})
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, envSecret, noFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q, want default", cfg.ListenAddr)
	}
	if cfg.Engine.Bin != "go2rtc" || cfg.Engine.WebRTCPort != 8555 {
		t.Errorf("engine defaults = %+v", cfg.Engine)
	}
	if cfg.Auth.SessionTTL != 30*24*time.Hour || cfg.Auth.Secret != testSecret {
		t.Errorf("auth = %+v", cfg.Auth)
	}
	if !cfg.KeepAwake {
		t.Error("keep_awake must default to true")
	}
	if len(cfg.Cameras) != 0 {
		t.Errorf("expected no cameras by default")
	}
}

func TestLoadYAMLFileAndRelativePaths(t *testing.T) {
	cfg, err := Load([]string{"-config", "/etc/wmd/watchmydog.yaml"}, env(nil), sampleFiles())
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
	if cfg.Auth.SecretFile != "/etc/wmd/secret" || cfg.Auth.Secret != testSecret {
		t.Errorf("auth secret not read from file next to config: %+v", cfg.Auth)
	}
	if cfg.Auth.SessionTTL != 48*time.Hour {
		t.Errorf("SessionTTL = %v", cfg.Auth.SessionTTL)
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

func TestSecretEnvOverridesFile(t *testing.T) {
	e := env(map[string]string{"WATCHMYDOG_SECRET": "  from-the-environment  "})
	cfg, err := Load([]string{"-config", "/etc/wmd/watchmydog.yaml"}, e, sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Secret != "from-the-environment" {
		t.Errorf("Secret = %q, want env value trimmed", cfg.Auth.Secret)
	}
}

func TestMissingSecretIsAnError(t *testing.T) {
	_, err := Load(nil, env(nil), noFile)
	if err == nil || !strings.Contains(err.Error(), "no secret configured") {
		t.Errorf("expected missing-secret error, got %v", err)
	}
	// Secret file configured but absent: the error says how to create it.
	only := files(map[string]string{"/etc/wmd/watchmydog.yaml": sampleYAML})
	_, err = Load([]string{"-config", "/etc/wmd/watchmydog.yaml"}, env(nil), only)
	if err == nil || !strings.Contains(err.Error(), "gen-secret /etc/wmd/secret") {
		t.Errorf("expected gen-secret hint, got %v", err)
	}
}

func TestShortSecretIsAnError(t *testing.T) {
	_, err := Load(nil, env(map[string]string{"WATCHMYDOG_SECRET": "short"}), noFile)
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Errorf("expected short-secret error, got %v", err)
	}
}

func TestAuthDisabledNeedsNoSecret(t *testing.T) {
	yml := "auth:\n  disabled: true\n  secret_file: missing\n"
	cfg, err := Load([]string{"-config", "c.yaml"}, env(nil), files(map[string]string{"c.yaml": yml}))
	if err != nil {
		t.Fatalf("disabled auth should not need a secret: %v", err)
	}
	if !cfg.Auth.Disabled || cfg.Auth.Secret != "" {
		t.Errorf("auth = %+v", cfg.Auth)
	}
}

func TestConfigPathFromEnv(t *testing.T) {
	e := env(map[string]string{"WATCHMYDOG_CONFIG": "/etc/wmd/watchmydog.yaml"})
	cfg, err := Load(nil, e, sampleFiles())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("config from env path not loaded: %q", cfg.ListenAddr)
	}
}

func TestPrecedenceFlagOverEnvOverFile(t *testing.T) {
	e := env(map[string]string{"WATCHMYDOG_LISTEN": "0.0.0.0:9000"})
	cfg, err := Load([]string{"-config", "/etc/wmd/watchmydog.yaml"}, e, sampleFiles())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("env should beat file, got %q", cfg.ListenAddr)
	}
	cfg, err = Load([]string{"-config", "/etc/wmd/watchmydog.yaml", "-listen", ":1234"}, e, sampleFiles())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":1234" {
		t.Errorf("flag should beat env, got %q", cfg.ListenAddr)
	}
}

func TestUnknownYAMLFieldIsAnError(t *testing.T) {
	_, err := Load([]string{"-config", "c.yaml"}, envSecret, files(map[string]string{"c.yaml": "listen: ':1'\nbogus: 1\n"}))
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
	_, err := Load([]string{"-config", "c.yaml"}, envSecret, files(map[string]string{"c.yaml": bad}))
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
	if _, err := Load([]string{"-config", "missing.yaml"}, envSecret, noFile); err == nil {
		t.Error("expected error")
	}
}

func TestLoadRejectsUnknownFlag(t *testing.T) {
	if _, err := Load([]string{"-bogus"}, envSecret, noFile); err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestDeployExampleConfigParses(t *testing.T) {
	const path = "../../deploy/watchmydog.example.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("example config not present:", err)
	}
	// The example's secret file does not exist in the repo; any other path
	// read returns a stand-in secret.
	read := func(p string) ([]byte, error) {
		if p == path {
			return data, nil
		}
		return []byte(testSecret), nil
	}
	cfg, err := Load([]string{"-config", path}, env(nil), read)
	if err != nil {
		t.Fatalf("deploy/watchmydog.example.yaml must load: %v", err)
	}
	if cfg.Auth.Disabled {
		t.Error("the example config must not disable auth")
	}
}

func TestKeepAwakeCanBeDisabled(t *testing.T) {
	cfg, err := Load([]string{"-config", "c.yaml"}, envSecret, files(map[string]string{"c.yaml": "keep_awake: false\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeepAwake {
		t.Error("keep_awake: false must be honoured")
	}
}

func TestSoundDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load([]string{"-config", "/etc/wmd/watchmydog.yaml"}, envSecret, sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	want := Sound{Enabled: true, ThresholdDBFS: -30, HysteresisDB: 6, MinDuration: time.Second, QuietTime: 5 * time.Second}
	if cfg.Sound != want {
		t.Errorf("defaults = %+v, want %+v", cfg.Sound, want)
	}
	if cfg.DBPath() != "/etc/wmd/data/watchmydog.db" {
		t.Errorf("DBPath = %q", cfg.DBPath())
	}

	// A partial section overrides only what it names.
	yaml := sampleYAML + "sound:\n  threshold_dbfs: -42.5\n  min_duration: 300ms\n"
	cfg, err = Load([]string{"-config", "/c.yaml"}, envSecret, files(map[string]string{"/c.yaml": yaml, "/secret": testSecret}))
	if err != nil {
		t.Fatal(err)
	}
	want.ThresholdDBFS, want.MinDuration = -42.5, 300*time.Millisecond
	if cfg.Sound != want {
		t.Errorf("overrides = %+v, want %+v", cfg.Sound, want)
	}
}

func TestSoundValidation(t *testing.T) {
	bad := map[string]string{
		"threshold_dbfs: 3":    "threshold_dbfs",
		"threshold_dbfs: -120": "threshold_dbfs",
		"hysteresis_db: -1":    "hysteresis_db",
		"min_duration: 10ms":   "min_duration",
		"quiet_time: 1h":       "quiet_time",
	}
	for line, wantErr := range bad {
		yaml := sampleYAML + "sound:\n  " + line + "\n"
		_, err := Load([]string{"-config", "/c.yaml"}, envSecret, files(map[string]string{"/c.yaml": yaml}))
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%q: err = %v, want mention of %s", line, err, wantErr)
		}
	}
	// Switched off, the values are not checked.
	yaml := sampleYAML + "sound:\n  enabled: false\n  threshold_dbfs: 3\n"
	if _, err := Load([]string{"-config", "/c.yaml"}, envSecret, files(map[string]string{"/c.yaml": yaml})); err != nil {
		t.Errorf("disabled sound section must not be validated: %v", err)
	}
}

func TestPushDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load([]string{"-config", "/etc/wmd/watchmydog.yaml"}, envSecret, sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	want := Push{Enabled: true, Cooldown: 5 * time.Minute, Contact: "watchmydog@example.invalid"}
	if cfg.Push != want {
		t.Errorf("defaults = %+v, want %+v", cfg.Push, want)
	}
	if w, err := cfg.Push.QuietHours.Window(); w != nil || err != nil {
		t.Errorf("default quiet hours = %v, %v; want none", w, err)
	}
	if cfg.VAPIDKeysPath() != "/etc/wmd/data/vapid.json" {
		t.Errorf("VAPIDKeysPath = %q", cfg.VAPIDKeysPath())
	}

	yaml := sampleYAML + "push:\n  cooldown: 0s\n  quiet_hours:\n    start: \"23:00\"\n    end: \"07:30\"\n  contact: https://example.com/me\n"
	cfg, err = Load([]string{"-config", "/c.yaml"}, envSecret, files(map[string]string{"/c.yaml": yaml}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Push.Enabled || cfg.Push.Cooldown != 0 || cfg.Push.Contact != "https://example.com/me" {
		t.Errorf("overrides = %+v", cfg.Push)
	}
	w, err := cfg.Push.QuietHours.Window()
	if err != nil || w == nil || w.StartMin != 23*60 || w.EndMin != 7*60+30 {
		t.Errorf("quiet hours = %+v, %v", w, err)
	}
}

func TestPushValidation(t *testing.T) {
	bad := map[string]string{
		"cooldown: -1s":                                  "cooldown",
		"cooldown: 48h":                                  "cooldown",
		"contact: nobody":                                "contact",
		"contact: http://example.com":                    "contact",
		"quiet_hours: {start: \"23:00\"}":                "quiet_hours",
		"quiet_hours: {start: \"11pm\", end: \"07:00\"}": "quiet_hours",
	}
	for line, wantErr := range bad {
		yaml := sampleYAML + "push:\n  " + line + "\n"
		_, err := Load([]string{"-config", "/c.yaml"}, envSecret, files(map[string]string{"/c.yaml": yaml}))
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%q: err = %v, want mention of %s", line, err, wantErr)
		}
	}
	yaml := sampleYAML + "push:\n  enabled: false\n  contact: nobody\n"
	if _, err := Load([]string{"-config", "/c.yaml"}, envSecret, files(map[string]string{"/c.yaml": yaml})); err != nil {
		t.Errorf("disabled push section must not be validated: %v", err)
	}
}

func TestRecordingDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load(nil, env(map[string]string{"WATCHMYDOG_SECRET": testSecret}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	want := Recording{
		Enabled: true, PreRoll: 15 * time.Second, PostRoll: 15 * time.Second,
		MaxClip: 5 * time.Minute, MaxDiskMB: 10000, KeepFor: 720 * time.Hour,
	}
	if cfg.Recording != want {
		t.Errorf("defaults = %+v, want %+v", cfg.Recording, want)
	}
	if got := cfg.RecordingsDir(); got != "data/recordings" {
		t.Errorf("RecordingsDir = %q", got)
	}

	yaml := "auth: {disabled: true}\nrecording:\n  pre_roll: 5s\n  max_clip: 2m\n  max_disk_mb: 500\n  keep_for: 0s\n"
	cfg, err = Load([]string{"-config", "/etc/wmd.yaml"}, env(nil), files(map[string]string{"/etc/wmd.yaml": yaml}))
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Recording
	if !r.Enabled || r.PreRoll != 5*time.Second || r.PostRoll != 15*time.Second || r.MaxClip != 2*time.Minute || r.MaxDiskMB != 500 || r.KeepFor != 0 {
		t.Errorf("overrides = %+v", r)
	}
}

func TestRecordingValidation(t *testing.T) {
	for name, section := range map[string]string{
		"pre_roll too long":        "pre_roll: 2m",
		"negative post_roll":       "post_roll: -1s",
		"max_clip too short":       "max_clip: 10s",
		"max_clip too long":        "max_clip: 2h",
		"max_clip not above rolls": "{pre_roll: 30s, post_roll: 30s, max_clip: 1m}",
		"tiny disk":                "max_disk_mb: 10",
		"negative keep_for":        "keep_for: -1h",
	} {
		yaml := "auth: {disabled: true}\nrecording:\n  " + section + "\n"
		if strings.HasPrefix(section, "{") {
			yaml = "auth: {disabled: true}\nrecording: " + section + "\n"
		}
		_, err := Load([]string{"-config", "/c.yaml"}, env(nil), files(map[string]string{"/c.yaml": yaml}))
		if err == nil || !strings.Contains(err.Error(), "recording.") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Switched off, the values are not checked.
	yaml := "auth: {disabled: true}\nrecording: {enabled: false, max_disk_mb: 1}\n"
	if _, err := Load([]string{"-config", "/c.yaml"}, env(nil), files(map[string]string{"/c.yaml": yaml})); err != nil {
		t.Errorf("disabled recording: %v", err)
	}
}

func TestTalkDefaultsOverridesAndValidation(t *testing.T) {
	load := func(yaml string) (Config, error) {
		return Load([]string{"-config", "/c.yaml"}, env(nil), files(map[string]string{"/c.yaml": "auth: {disabled: true}\n" + yaml}))
	}
	cfg, err := load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Talk != (Talk{Enabled: true, Volume: 100}) {
		t.Errorf("defaults = %+v", cfg.Talk)
	}
	if got := cfg.FFplayPath(); got != "ffplay" {
		t.Errorf("FFplayPath = %q, want ffplay (ffmpeg is found via PATH too)", got)
	}

	cfg, err = load("go2rtc: {ffmpeg_bin: /opt/homebrew/bin/ffmpeg}\ntalk: {volume: 60}\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Talk.Volume != 60 || cfg.FFplayPath() != "/opt/homebrew/bin/ffplay" {
		t.Errorf("talk = %+v, FFplayPath = %q", cfg.Talk, cfg.FFplayPath())
	}
	cfg, _ = load("talk: {ffplay_bin: /usr/local/bin/ffplay}\n")
	if cfg.FFplayPath() != "/usr/local/bin/ffplay" {
		t.Errorf("FFplayPath = %q", cfg.FFplayPath())
	}

	for _, bad := range []string{"talk: {volume: 0}\n", "talk: {volume: 101}\n"} {
		if _, err := load(bad); err == nil || !strings.Contains(err.Error(), "talk.volume") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if _, err := load("talk: {enabled: false, volume: 0}\n"); err != nil {
		t.Errorf("disabled talk: %v", err)
	}
}
