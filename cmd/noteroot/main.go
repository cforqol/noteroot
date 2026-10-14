package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"$MOD/internal/config"
	"$MOD/internal/httpapi"
	"$MOD/internal/log"
	"$MOD/internal/notebooks"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := log.New(cfg.LogLevel)

	reg, problems := notebooks.Open(cfg.DataDir)
	for _, p := range problems {
		logger.Warn("notebook problem", "err", p)
	}
	logger.Info("started", "notebooks", len(reg.List()), "auth", cfg.AuthMode)

	srv := httpapi.New(cfg, logger, reg)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}
