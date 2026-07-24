// Command server runs the DispatchLab backend: the REST command API and the
// WebSocket stream for any number of concurrent simulations.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	dispatchhttp "dispatchlab/internal/transport/http"

	"dispatchlab/internal/service"
	"dispatchlab/internal/store"
	"dispatchlab/internal/store/postgres"
	"dispatchlab/internal/telemetry"
)

const (
	defaultAddr = ":8080"
	// maxSimulations bounds guest-created simulations until phase 6 adds
	// session-scoped quotas; a flat cap is the simplest safe default.
	maxSimulations = 50
	// shutdownTimeout bounds how long in-flight requests get to finish.
	shutdownTimeout = 10 * time.Second
)

func main() {
	logger := telemetry.NewLogger(env("LOG_FORMAT", "json"), logLevel())
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metrics := telemetry.NewMetrics()

	eventStore, err := openStore(ctx, logger)
	if err != nil {
		return err
	}
	defer func() { _ = eventStore.Close() }()

	mgr := service.NewManagerWithConfig(service.ManagerConfig{
		Max:     maxSimulations,
		Store:   eventStore,
		Metrics: metrics,
		Logger:  logger,
	})
	defer mgr.Shutdown()

	server := dispatchhttp.NewServerWithConfig(dispatchhttp.ServerConfig{
		Manager:     mgr,
		Comparisons: service.NewComparisonsWithStore(eventStore, metrics, logger),
		Store:       eventStore,
		Metrics:     metrics,
		Logger:      logger,
	})

	addr := env("ADDR", defaultAddr)
	httpServer := &http.Server{Addr: addr, Handler: server.Routes()}

	errs := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// openStore connects to Postgres when DATABASE_URL is set and falls back to
// the in-memory store otherwise, so the demo runs locally with no database
// installed — replays then only last as long as the process.
func openStore(ctx context.Context, logger *slog.Logger) (store.Store, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		logger.Warn("DATABASE_URL is not set, falling back to in-memory storage; replays will not survive a restart")
		return store.NewMemory(), nil
	}

	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	pg, err := postgres.Open(connectCtx, url)
	if err != nil {
		return nil, err
	}
	logger.Info("connected to postgres and applied migrations")
	return pg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func logLevel() slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return slog.LevelInfo
	}
	return level
}
