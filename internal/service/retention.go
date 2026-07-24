package service

import (
	"context"
	"log/slog"
	"time"

	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

// DefaultSweepInterval is how often expired runs and sessions are collected.
// Retention is measured in hours, so sweeping every few minutes is prompt
// enough while keeping the query rate negligible.
const DefaultSweepInterval = 5 * time.Minute

// Retention deletes anonymous runs and dead sessions once they age out. It is
// what keeps a public demo's event log from growing without bound, and it is
// the reason a guest run is not kept forever the way a showcase run is.
type Retention struct {
	store    store.Store
	sessions *Sessions
	metrics  *telemetry.Metrics
	logger   *slog.Logger
	interval time.Duration
}

// RetentionConfig configures the sweeper.
type RetentionConfig struct {
	Store    store.Store
	Sessions *Sessions
	Metrics  *telemetry.Metrics
	Logger   *slog.Logger
	Interval time.Duration
}

// NewRetention returns a sweeper.
func NewRetention(cfg RetentionConfig) *Retention {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultSweepInterval
	}
	return &Retention{
		store:    cfg.Store,
		sessions: cfg.Sessions,
		metrics:  cfg.Metrics,
		logger:   cfg.Logger,
		interval: cfg.Interval,
	}
}

// Run sweeps on an interval until ctx is cancelled. It sweeps once on start
// so a process that restarts often still collects what the last one left.
func (r *Retention) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Sweep(ctx)
		}
	}
}

// Sweep removes everything currently expired and reports what went. A
// failure is logged and counted rather than returned: retention running late
// is not a reason to take the process down.
func (r *Retention) Sweep(ctx context.Context) store.PurgeResult {
	now := time.Now().UTC()

	// with no database, sessions live in the session service's own map and
	// there are no persisted runs to collect.
	if r.store == nil {
		if r.sessions == nil {
			return store.PurgeResult{}
		}
		removed := r.sessions.PurgeFallback(now)
		if removed > 0 {
			r.logger.Info("swept expired guest sessions", "sessions", removed)
		}
		return store.PurgeResult{Sessions: removed}
	}

	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	result, err := r.store.PurgeExpired(sweepCtx, now)
	if err != nil {
		r.metrics.PersistenceErrors().Inc()
		r.logger.Error("retention sweep failed", "error", err)
		return store.PurgeResult{}
	}

	if result.Simulations > 0 || result.Sessions > 0 {
		r.logger.Info("swept expired runs and sessions",
			"simulations", result.Simulations, "sessions", result.Sessions)
	}
	return result
}
