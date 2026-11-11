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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maikomr/watchmydog/internal/adapters/go2rtc"
	"github.com/maikomr/watchmydog/internal/buildinfo"
	"github.com/maikomr/watchmydog/internal/config"
	"github.com/maikomr/watchmydog/internal/httpapi"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "watchmydog:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := config.LoadFromOS()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.NewHandler(httpapi.Deps{
			Version: buildinfo.Version,
			Commit:  buildinfo.Commit,
			Logger:  logger,
			Cameras: cameras,
			Engine:  engine,
			WebDir:  cfg.WebDir,
		}),
		ReadHeaderTimeout: 10 * time.Second,
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
