// Package config loads runtime configuration for the watchmydog service.
//
// Precedence, lowest to highest: built-in defaults, YAML file, environment
// variables, command-line flags. Only operational knobs (listen address,
// config path) have env/flag forms; cameras and engine settings live in YAML.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maikomr/watchmydog/internal/domain"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// ListenAddr is the host:port the HTTP API binds to.
	ListenAddr string `yaml:"listen"`
	// DataDir holds generated files and, later, recordings and the database.
	DataDir string `yaml:"data_dir"`
	// WebDir is the built web client to serve at "/". Empty disables it.
	WebDir string `yaml:"web_dir"`
	// KeepAwake makes the service prevent idle system sleep while it runs,
	// so the host stays reachable (a sleeping Mac is offline for Tailscale).
	KeepAwake bool `yaml:"keep_awake"`
	// Auth configures login.
	Auth Auth `yaml:"auth"`
	// Engine configures the go2rtc media engine.
	Engine Engine `yaml:"go2rtc"`
	// Cameras is the list of cameras to stream.
	Cameras []Camera `yaml:"cameras"`
}

// MinSecretLength is the shortest accepted login secret.
const MinSecretLength = 12

// Auth is the login section.
type Auth struct {
	// Disabled turns authentication off entirely. Local development only.
	Disabled bool `yaml:"disabled"`
	// SecretFile holds the shared login secret on one line. Create it with
	// `watchmydog gen-secret <file>`. WATCHMYDOG_SECRET overrides it.
	SecretFile string `yaml:"secret_file"`
	// SessionTTL is how long a login stays valid on a device.
	SessionTTL time.Duration `yaml:"session_ttl"`
	// Secret is the resolved secret. Never read from or written to YAML.
	Secret string `yaml:"-"`
}

// Engine is the go2rtc section.
type Engine struct {
	// Bin is the go2rtc executable (name resolved via PATH or a path).
	Bin string `yaml:"bin"`
	// APIAddr is where go2rtc's HTTP API listens; the Go service proxies it.
	APIAddr string `yaml:"api"`
	// RTSPAddr is go2rtc's RTSP listener, used internally by later increments.
	RTSPAddr string `yaml:"rtsp"`
	// WebRTCPort is the ICE port go2rtc advertises; must be reachable by viewers.
	WebRTCPort int `yaml:"webrtc_port"`
	// FFmpegBin is the ffmpeg executable used for encoding and audio capture.
	FFmpegBin string `yaml:"ffmpeg_bin"`
	// CaptureBin is the wmd-capture helper executable.
	CaptureBin string `yaml:"capture_bin"`
	// LogLevel is go2rtc's log level (trace, debug, info, warn, error).
	LogLevel string `yaml:"log_level"`
}

// Camera is the YAML shape of a camera; converted to domain.Camera by Load.
type Camera struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Video struct {
		Device      string `yaml:"device"`
		Width       int    `yaml:"width"`
		Height      int    `yaml:"height"`
		FPS         int    `yaml:"fps"`
		BitrateKbps int    `yaml:"bitrate_kbps"`
		Low         *struct {
			Width  int `yaml:"width"`
			Height int `yaml:"height"`
		} `yaml:"low"`
	} `yaml:"video"`
	Audio struct {
		Device string `yaml:"device"`
	} `yaml:"audio"`
}

// Defaults returns the configuration used when nothing is overridden.
func Defaults() Config {
	return Config{
		ListenAddr: "127.0.0.1:8080",
		DataDir:    "data",
		WebDir:     "web/dist",
		KeepAwake:  true,
		Auth:       Auth{SessionTTL: 30 * 24 * time.Hour},
		Engine: Engine{
			Bin:        "go2rtc",
			APIAddr:    "127.0.0.1:1984",
			RTSPAddr:   "127.0.0.1:8554",
			WebRTCPort: 8555,
			FFmpegBin:  "ffmpeg",
			CaptureBin: "wmd-capture",
			LogLevel:   "info",
		},
	}
}

// Load resolves configuration from a YAML file (flag -config or env
// WATCHMYDOG_CONFIG), environment variables and command-line arguments.
// getenv and readFile are injected so tests do not touch the real
// environment or filesystem.
func Load(args []string, getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	cfg := Defaults()

	// First pass over flags only to find the config path; values parsed
	// here are re-applied after the file so flags keep the highest precedence.
	fs := flag.NewFlagSet("watchmydog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", getenv("WATCHMYDOG_CONFIG"), "path to YAML config file")
	listen := fs.String("listen", "", "address to listen on (host:port)")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	if *configPath != "" {
		data, err := readFile(*configPath)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		dec := yaml.NewDecoder(bytesReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse config %s: %w", *configPath, err)
		}
		// Relative paths in the file are relative to the file's directory, so
		// the service behaves the same regardless of its working directory.
		base := filepath.Dir(*configPath)
		cfg.DataDir = resolveRel(base, cfg.DataDir)
		cfg.WebDir = resolveRel(base, cfg.WebDir)
		cfg.Auth.SecretFile = resolveRel(base, cfg.Auth.SecretFile)
	}

	if v := getenv("WATCHMYDOG_LISTEN"); v != "" {
		cfg.ListenAddr = v
	}
	if *listen != "" {
		cfg.ListenAddr = *listen
	}

	if !cfg.Auth.Disabled {
		if v := getenv("WATCHMYDOG_SECRET"); v != "" {
			cfg.Auth.Secret = strings.TrimSpace(v)
		} else if cfg.Auth.SecretFile != "" {
			data, err := readFile(cfg.Auth.SecretFile)
			if err != nil {
				return Config{}, fmt.Errorf("read auth secret: %w (create it with: watchmydog gen-secret %s)", err, cfg.Auth.SecretFile)
			}
			cfg.Auth.Secret = strings.TrimSpace(string(data))
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadFromOS is Load wired to the real process environment.
func LoadFromOS() (Config, error) {
	return Load(os.Args[1:], os.Getenv, os.ReadFile)
}

// Validate checks the resolved configuration.
func (c Config) Validate() error {
	var errs []error
	if c.ListenAddr == "" {
		errs = append(errs, errors.New("listen address must not be empty"))
	}
	if !c.Auth.Disabled {
		switch {
		case c.Auth.Secret == "":
			errs = append(errs, errors.New("auth: no secret configured; set auth.secret_file or WATCHMYDOG_SECRET "+
				"(generate one with `watchmydog gen-secret data/secret`), or auth.disabled: true for local development"))
		case len(c.Auth.Secret) < MinSecretLength:
			errs = append(errs, fmt.Errorf("auth: secret must be at least %d characters", MinSecretLength))
		}
	}
	if c.Auth.SessionTTL <= 0 {
		errs = append(errs, errors.New("auth.session_ttl must be positive"))
	}
	if c.Engine.Bin == "" || c.Engine.FFmpegBin == "" || c.Engine.CaptureBin == "" {
		errs = append(errs, errors.New("go2rtc.bin, go2rtc.ffmpeg_bin and go2rtc.capture_bin are required"))
	}
	if c.Engine.APIAddr == "" || c.Engine.RTSPAddr == "" {
		errs = append(errs, errors.New("go2rtc.api and go2rtc.rtsp are required"))
	}
	if c.Engine.WebRTCPort <= 0 || c.Engine.WebRTCPort > 65535 {
		errs = append(errs, errors.New("go2rtc.webrtc_port must be 1-65535"))
	}
	seen := map[string]bool{}
	for _, cam := range c.Cameras {
		if seen[cam.ID] {
			errs = append(errs, fmt.Errorf("duplicate camera id %q", cam.ID))
		}
		seen[cam.ID] = true
		if err := cam.Domain().Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Domain converts the YAML camera into the domain entity.
func (c Camera) Domain() domain.Camera {
	cam := domain.Camera{
		ID:   domain.CameraID(c.ID),
		Name: c.Name,
		Video: domain.VideoSource{
			Device:      c.Video.Device,
			Width:       c.Video.Width,
			Height:      c.Video.Height,
			FPS:         c.Video.FPS,
			BitrateKbps: c.Video.BitrateKbps,
		},
		Audio: domain.AudioSource{Device: c.Audio.Device},
	}
	if c.Video.Low != nil {
		cam.Video.Low = &domain.LowRes{Width: c.Video.Low.Width, Height: c.Video.Low.Height}
	}
	return cam
}

// DomainCameras converts all configured cameras.
func (c Config) DomainCameras() []domain.Camera {
	out := make([]domain.Camera, 0, len(c.Cameras))
	for _, cam := range c.Cameras {
		out = append(out, cam.Domain())
	}
	return out
}

func resolveRel(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
