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
	"strconv"
	"strings"
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
	// maxSimulations is the process-wide ceiling, on top of the per-session
	// quota. The quota stops one visitor taking more than their share; this
	// stops many visitors between them exhausting the machine.
	maxSimulations = 50
	// shutdownTimeout bounds how long in-flight requests get to finish.
	shutdownTimeout = 10 * time.Second
	// defaultRatePerSecond and defaultRateBurst are generous for a person
	// clicking a map and restrictive for a script hammering the API.
	defaultRatePerSecond = 10
	defaultRateBurst     = 30
	// readHeaderTimeout bounds how long a client may take to send its
	// headers, which is what stops a slow-loris connection from occupying a
	// server goroutine indefinitely.
	readHeaderTimeout = 10 * time.Second
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

	// the curated runs are provisioned before the listener opens, so a
	// showcase URL never 404s on a freshly deployed instance.
	if err := service.ProvisionShowcases(ctx, eventStore, service.DefaultShowcaseRuns(), metrics, logger); err != nil {
		return err
	}

	sessions := service.NewSessions(service.SessionsConfig{
		Store:   eventStore,
		Metrics: metrics,
		Logger:  logger,
	})

	mgr := service.NewManagerWithConfig(service.ManagerConfig{
		Max:      maxSimulations,
		Store:    eventStore,
		Metrics:  metrics,
		Logger:   logger,
		Sessions: sessions,
	})
	defer mgr.Shutdown()

	retention := service.NewRetention(service.RetentionConfig{
		Store:    eventStore,
		Sessions: sessions,
		Metrics:  metrics,
		Logger:   logger,
	})
	go retention.Run(ctx)

	server := dispatchhttp.NewServerWithConfig(dispatchhttp.ServerConfig{
		Manager:           mgr,
		Comparisons:       service.NewComparisonsWithStore(eventStore, metrics, logger),
		Store:             eventStore,
		Sessions:          sessions,
		Metrics:           metrics,
		Logger:            logger,
		AllowedOrigins:    splitList(os.Getenv("ALLOWED_ORIGINS")),
		RequestsPerSecond: envFloat("RATE_LIMIT_PER_SECOND", defaultRatePerSecond),
		RequestBurst:      envFloat("RATE_LIMIT_BURST", defaultRateBurst),
	})

	addr := listenAddr()
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

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

// listenAddr honors an explicit ADDR first, then Render's PORT convention
// (the platform assigns the port and expects the app to bind to it), then
// falls back to the default.
func listenAddr() string {
	if v := os.Getenv("ADDR"); v != "" {
		return v
	}
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return defaultAddr
}

func envFloat(key string, fallback float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// splitList parses a comma-separated environment value, dropping blanks so a
// trailing comma is not read as an empty entry.
func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func logLevel() slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return slog.LevelInfo
	}
	return level
}
