// Command watchmydog is the edge service that runs on the Mac mini.
//
// main() only wires adapters together and manages process lifecycle; all
// behaviour lives in internal/ packages so it can be tested without a
// running process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/auth/sharedsecret"
	"github.com/maikomr/watchmydog/internal/adapters/go2rtc"
	"github.com/maikomr/watchmydog/internal/adapters/notify/webpush"
	"github.com/maikomr/watchmydog/internal/adapters/power"
	recorder "github.com/maikomr/watchmydog/internal/adapters/recorder/ffmpeg"
	"github.com/maikomr/watchmydog/internal/adapters/sound/threshold"
	"github.com/maikomr/watchmydog/internal/adapters/speaker/ffplay"
	"github.com/maikomr/watchmydog/internal/adapters/store/sqlite"
	"github.com/maikomr/watchmydog/internal/app/alerts"
	"github.com/maikomr/watchmydog/internal/app/cameras"
	"github.com/maikomr/watchmydog/internal/app/clips"
	"github.com/maikomr/watchmydog/internal/app/monitor"
	"github.com/maikomr/watchmydog/internal/app/talk"
	"github.com/maikomr/watchmydog/internal/buildinfo"
	"github.com/maikomr/watchmydog/internal/config"
	"github.com/maikomr/watchmydog/internal/domain"
	"github.com/maikomr/watchmydog/internal/httpapi"
	"github.com/maikomr/watchmydog/internal/ports"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "watchmydog:", err)
		os.Exit(1)
	}
}

func run() error {
	// Subcommands that do not start the service.
	if len(os.Args) > 1 && os.Args[1] == "gen-secret" {
		return genSecret(os.Args[2:])
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := config.LoadFromOS()
	if err != nil {
		return err
	}

	var auth ports.Authenticator
	if cfg.Auth.Disabled {
		logger.Warn("authentication DISABLED (auth.disabled: true): anyone who can reach the service can watch")
	} else {
		a, err := sharedsecret.New(cfg.Auth.Secret, cfg.Auth.SessionTTL, nil)
		if err != nil {
			return fmt.Errorf("auth: %w", err)
		}
		auth = a
		warnIfReadableByOthers(logger, cfg.Auth.SecretFile)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Keep the host awake while serving (ADR-0006). Released on exit; the
	// kernel also drops it if the process dies.
	if cfg.KeepAwake {
		if !power.Supported() {
			logger.Warn("keep_awake is not implemented on this platform")
		} else if a, err := power.PreventIdleSleep("watchmydog: serving the camera stream"); err != nil {
			logger.Error("could not prevent idle sleep; the host may go offline when idle", "err", err)
		} else {
			defer a.Release()
			logger.Info("preventing idle system sleep while running")
		}
	}

	// The database is opened before the media engine: it knows which
	// cameras the owner released, and those must not be opened at start.
	// Closed last, after the monitor has saved an open event (see below).
	store, err := sqlite.Open(ctx, cfg.DBPath())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close database", "err", err)
		}
	}()

	cams := cfg.DomainCameras()
	edge := cfg.Edge()
	logger.Info("role", "role", cfg.Role)

	// Media engine: supervised go2rtc process. Only the edge has one; a
	// cloud process serves the API without media (ADR-0011).
	var engine ports.StreamEngine
	if edge {
		eng := cameras.Remember(go2rtc.New(go2rtc.Settings{
			Bin:        cfg.Engine.Bin,
			APIAddr:    cfg.Engine.APIAddr,
			RTSPAddr:   cfg.Engine.RTSPAddr,
			WebRTCPort: cfg.Engine.WebRTCPort,
			FFmpegBin:  cfg.Engine.FFmpegBin,
			CaptureBin: cfg.Engine.CaptureBin,
			LogLevel:   cfg.Engine.LogLevel,
		}, cfg.DataDir, logger.With("component", "engine")), store, logger.With("component", "cameras"))
		if err := eng.Start(ctx, cams, nil); err != nil {
			return fmt.Errorf("start media engine: %w", err)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := eng.Stop(shutdownCtx); err != nil {
				logger.Error("stop media engine", "err", err)
			}
		}()
		logger.Info("media engine ready", "cameras", len(cams))
		engine = eng
	}

	// Event history and sound monitoring (ADR-0007).
	if n, err := store.CloseOpenEvents(ctx); err != nil {
		logger.Error("could not close events left open by the last run", "err", err)
	} else if n > 0 {
		logger.Warn("closed events left open by the last run", "count", n)
	}
	rules := threshold.Rules{
		ThresholdDBFS: cfg.Sound.ThresholdDBFS,
		HysteresisDB:  cfg.Sound.HysteresisDB,
		MinDuration:   cfg.Sound.MinDuration,
		QuietTime:     cfg.Sound.QuietTime,
	}
	mon := monitor.New(store, monitor.Info{
		Enabled:       cfg.Sound.Enabled,
		ThresholdDBFS: rules.ThresholdDBFS,
		ReleaseDBFS:   rules.ReleaseDBFS(),
	}, logger.With("component", "monitor"))

	// Notifications (ADR-0008). Hooked into the monitor before it runs.
	notify, err := newAlerts(cfg, store, cams, logger)
	if err != nil {
		return err
	}
	mon.OnEventStart(notify.EventStarted)
	go notify.Run(ctx)

	// Talking through the loudspeaker (ADR-0010) and recording (ADR-0009)
	// are edge features; the cloud leaves their routes out.
	var (
		talkSvc *talk.Service
		clipSvc *clips.Service
	)
	talkDone := make(chan struct{})
	clipsDone := make(chan struct{})
	if edge {
		// The microphone hears the owner's voice as noise, so notifications
		// are held back meanwhile.
		talkSvc = newTalk(cfg, logger)
		talkSvc.OnActive(notify.HoldOff)
		go func() {
			defer close(talkDone)
			talkSvc.Run(ctx)
		}()

		clipSvc, err = newClips(cfg, store, logger)
		if err != nil {
			return err
		}
		mon.OnEventStart(clipSvc.EventStarted)
		mon.OnEventEnd(clipSvc.EventEnded)
		go func() {
			defer close(clipsDone)
			clipSvc.Run(ctx, cams)
		}()
	} else {
		close(talkDone)
		close(clipsDone)
	}

	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		if !cfg.Sound.Enabled {
			logger.Info("sound detection disabled (sound.enabled: false)")
			return
		}
		logger.Info("sound detection on", "threshold_dbfs", rules.ThresholdDBFS, "release_dbfs", rules.ReleaseDBFS(),
			"min_duration", rules.MinDuration, "quiet_time", rules.QuietTime)
		detector := threshold.New(threshold.Settings{
			FFmpegBin: cfg.Engine.FFmpegBin,
			RTSPAddr:  cfg.Engine.RTSPAddr,
		}, rules, logger.With("component", "sound"))
		mon.Run(ctx, detector, cams)
	}()
	// Runs before the engine is stopped and the database is closed (defers
	// are last-in, first-out): stop listening, let an open event be saved
	// and let the recorders and a clip being written finish.
	defer func() {
		stop()
		<-monitorDone
		<-clipsDone
		<-talkDone
	}()

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.NewHandler(httpapi.Deps{
			Version: buildinfo.Version,
			Commit:  buildinfo.Commit,
			Logger:  logger,
			Cameras: cams,
			Engine:  engine,
			Monitor: mon,
			Alerts:  notify,
			Clips:   clipSvc,
			Talk:    talkSvc,
			WebDir:  cfg.WebDir,
			Auth:    auth,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests inherit the shutdown signal, so long-lived responses
		// (the live event stream) end at once instead of holding up exit.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr, "version", buildinfo.Version, "commit", buildinfo.Commit)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// newTalk builds the talk service. A missing ffplay switches talking off
// instead of stopping the service: watching matters more.
func newTalk(cfg config.Config, logger *slog.Logger) *talk.Service {
	log := logger.With("component", "talk")
	rules := talk.Settings{
		Enabled:     cfg.Talk.Enabled,
		IdleTimeout: 3 * time.Second,
		MaxDuration: 2 * time.Minute,
		// The detector needs sound.min_duration to open an event and the
		// room echoes a little; hold notifications back a bit longer.
		HoldOff: cfg.Sound.MinDuration + cfg.Sound.QuietTime + 5*time.Second,
	}
	if !cfg.Talk.Enabled {
		logger.Info("talking disabled (talk.enabled: false)")
		return talk.New(nil, rules, log)
	}
	bin, err := exec.LookPath(cfg.FFplayPath())
	if err != nil {
		logger.Warn("talking is off: ffplay not found (it comes with ffmpeg; set talk.ffplay_bin)", "tried", cfg.FFplayPath(), "err", err)
		return talk.New(nil, rules, log)
	}
	logger.Info("talking on", "ffplay", bin, "volume", cfg.Talk.Volume)
	return talk.New(ffplay.New(ffplay.Settings{Bin: bin, Volume: cfg.Talk.Volume}, log), rules, log)
}

// newClips builds the recording service. With recording.enabled: false it
// still exists, so clips made earlier can be played and deleted.
func newClips(cfg config.Config, store ports.ClipStore, logger *slog.Logger) (*clips.Service, error) {
	r := cfg.Recording
	rules := clips.Settings{
		Enabled:   r.Enabled,
		PreRoll:   r.PreRoll,
		PostRoll:  r.PostRoll,
		MaxLength: r.MaxClip,
		MaxBytes:  r.MaxDiskMB * 1_000_000,
		KeepFor:   r.KeepFor,
	}
	switch {
	case !r.Enabled:
		logger.Info("recording disabled (recording.enabled: false)")
	case !cfg.Sound.Enabled:
		// Clips are made of noise events; without detection the camera
		// would run around the clock for nothing.
		rules.Enabled = false
		logger.Warn("recording is off because sound detection is off (sound.enabled: false)")
	default:
		logger.Info("recording on", "pre_roll", r.PreRoll, "post_roll", r.PostRoll, "max_clip", r.MaxClip,
			"max_disk_mb", r.MaxDiskMB, "keep_for", r.KeepFor, "dir", cfg.RecordingsDir())
	}
	ringLength := rules.RingLength()
	if !rules.Enabled {
		ringLength = time.Minute // unused, but the recorder wants a sane value
	}
	rec, err := recorder.New(recorder.Settings{
		FFmpegBin:  cfg.Engine.FFmpegBin,
		RTSPAddr:   cfg.Engine.RTSPAddr,
		Dir:        cfg.RecordingsDir(),
		RingLength: ringLength,
	}, logger.With("component", "recorder"))
	if err != nil {
		return nil, err
	}
	return clips.New(rec, store, rules, logger.With("component", "clips")), nil
}

// newAlerts builds the notification service. With push.enabled: false it
// still exists (the API then reports "disabled") but sends nothing.
func newAlerts(cfg config.Config, store ports.SubscriptionStore, cameras []domain.Camera, logger *slog.Logger) (*alerts.Service, error) {
	log := logger.With("component", "alerts")
	if !cfg.Push.Enabled {
		logger.Info("notifications disabled (push.enabled: false)")
		return alerts.New(store, nil, cameras, alerts.Settings{}, log), nil
	}
	keys, created, err := webpush.LoadOrCreateKeys(cfg.VAPIDKeysPath())
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	if created {
		logger.Info("created the key pair that signs notifications", "file", cfg.VAPIDKeysPath())
	}
	warnIfReadableByOthers(logger, cfg.VAPIDKeysPath())
	notifier, err := webpush.New(webpush.Settings{Keys: keys, Contact: cfg.Push.Contact})
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	quiet, err := cfg.Push.QuietHours.Window()
	if err != nil {
		return nil, fmt.Errorf("push.quiet_hours: %w", err)
	}
	quietLog := "none"
	if quiet != nil {
		quietLog = quiet.String()
	}
	logger.Info("notifications on", "cooldown", cfg.Push.Cooldown, "quiet_hours", quietLog)
	return alerts.New(store, notifier, cameras, alerts.Settings{
		Enabled:   true,
		PublicKey: notifier.PublicKey(),
		Cooldown:  cfg.Push.Cooldown,
		Quiet:     quiet,
	}, log), nil
}

// genSecret implements `watchmydog gen-secret <file>`: it creates a random
// login secret with mode 0600. It never prints the secret itself, so it does
// not end up in terminal scrollback or logs; read it with `cat <file>`.
func genSecret(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: watchmydog gen-secret <file>")
	}
	path := args[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := sharedsecret.CreateSecretFile(path); err != nil {
		return err
	}
	fmt.Printf("Login secret written to %s (mode 0600).\nShow it with: cat %s\n", path, path)
	return nil
}

// warnIfReadableByOthers flags a secret file other local users could read.
func warnIfReadableByOthers(logger *slog.Logger, path string) {
	if path == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if fi.Mode().Perm()&0o077 != 0 {
		logger.Warn("secret file is readable by other users; run chmod 600", "file", path, "mode", fi.Mode().Perm().String())
	}
}
