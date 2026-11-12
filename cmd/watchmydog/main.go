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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/auth/sharedsecret"
	"github.com/maikomr/watchmydog/internal/adapters/go2rtc"
	"github.com/maikomr/watchmydog/internal/adapters/power"
	"github.com/maikomr/watchmydog/internal/adapters/sound/threshold"
	"github.com/maikomr/watchmydog/internal/adapters/store/sqlite"
	"github.com/maikomr/watchmydog/internal/app/monitor"
	"github.com/maikomr/watchmydog/internal/buildinfo"
	"github.com/maikomr/watchmydog/internal/config"
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

	// Media engine: supervised go2rtc process.
	engine := go2rtc.New(go2rtc.Settings{
		Bin:        cfg.Engine.Bin,
		APIAddr:    cfg.Engine.APIAddr,
		RTSPAddr:   cfg.Engine.RTSPAddr,
		WebRTCPort: cfg.Engine.WebRTCPort,
		FFmpegBin:  cfg.Engine.FFmpegBin,
		CaptureBin: cfg.Engine.CaptureBin,
		LogLevel:   cfg.Engine.LogLevel,
	}, cfg.DataDir, logger.With("component", "engine"))
	cameras := cfg.DomainCameras()
	if err := engine.Start(ctx, cameras); err != nil {
		return fmt.Errorf("start media engine: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := engine.Stop(shutdownCtx); err != nil {
			logger.Error("stop media engine", "err", err)
		}
	}()
	logger.Info("media engine ready", "cameras", len(cameras))

	// Event history and sound monitoring (ADR-0007).
	store, err := sqlite.Open(ctx, cfg.DBPath())
	if err != nil {
		return fmt.Errorf("event store: %w", err)
	}
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
		mon.Run(ctx, detector, cameras)
	}()
	// Runs before the engine is stopped (defers are last-in, first-out):
	// stop listening, let an open event be saved, then close the database.
	defer func() {
		stop()
		<-monitorDone
		if err := store.Close(); err != nil {
			logger.Error("close event store", "err", err)
		}
	}()

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.NewHandler(httpapi.Deps{
			Version: buildinfo.Version,
			Commit:  buildinfo.Commit,
			Logger:  logger,
			Cameras: cameras,
			Engine:  engine,
			Monitor: mon,
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
