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

	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.NewHandler(httpapi.Deps{
			Version: buildinfo.Version,
			Commit:  buildinfo.Commit,
			Logger:  logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
