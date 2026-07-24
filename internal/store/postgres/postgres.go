// Package postgres implements the event, snapshot, and comparison store on
// PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"dispatchlab/internal/store"
)

// Store is a store.Store backed by a PostgreSQL connection pool.
type Store struct {
	pool *pgxpool.Pool
}

const (
	// statementTimeout is enforced by the database itself, so a query that
	// somehow escapes its context deadline still cannot hold a connection
	// open indefinitely.
	statementTimeout = 10 * time.Second
	// maxConns bounds the pool. The event log is written in batches by a
	// handful of goroutines, so a small pool is plenty and keeps a burst of
	// traffic from opening more connections than the database will accept.
	maxConns = 10
	// maxConnLifetime recycles connections so a long-lived process does not
	// hold one across a database restart or failover.
	maxConnLifetime = time.Hour
	maxConnIdleTime = 5 * time.Minute
)

// Open connects to the given database URL, verifies the connection, and
// brings the schema up to date. It is safe to call against an empty database.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	cfg.MaxConns = maxConns
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.MaxConnIdleTime = maxConnIdleTime
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(int(statementTimeout.Milliseconds()))

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	s := &Store{pool: pool}
	if err := s.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Pool exposes the underlying connection pool, used by tests that need to
// reset schema state between runs.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) CreateGuestSession(ctx context.Context, session store.GuestSession) error {
	const query = `
		insert into guest_sessions (token, created_at, last_seen_at, expires_at)
		values ($1, $2, $3, $4)
		on conflict (token) do nothing`

	now := time.Now().UTC()
	createdAt, lastSeen := session.CreatedAt, session.LastSeenAt
	if createdAt.IsZero() {
		createdAt = now
	}
	if lastSeen.IsZero() {
		lastSeen = now
	}
	_, err := s.pool.Exec(ctx, query, session.Token, createdAt, lastSeen, session.ExpiresAt)
	return err
}

func (s *Store) GetGuestSession(ctx context.Context, token string) (store.GuestSession, error) {
	const query = `select token, created_at, last_seen_at, expires_at from guest_sessions where token = $1`

	var session store.GuestSession
	err := s.pool.QueryRow(ctx, query, token).Scan(
		&session.Token, &session.CreatedAt, &session.LastSeenAt, &session.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GuestSession{}, store.ErrNotFound
	}
	if err != nil {
		return store.GuestSession{}, err
	}
	return session, nil
}

func (s *Store) TouchGuestSession(ctx context.Context, token string, lastSeen, expiresAt time.Time) error {
	const query = `update guest_sessions set last_seen_at = $2, expires_at = $3 where token = $1`

	tag, err := s.pool.Exec(ctx, query, token, lastSeen, expiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CreateSimulation(ctx context.Context, sim store.Simulation) error {
	const query = `
		insert into simulations (id, seed, drivers, strategy, created_at, completed_at, showcase, guest_token, expires_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		on conflict (id) do nothing`

	createdAt := sim.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var token any
	if sim.GuestToken != "" {
		token = sim.GuestToken
	}
	_, err := s.pool.Exec(ctx, query, sim.ID, sim.Seed, sim.Drivers, sim.Strategy,
		createdAt, sim.CompletedAt, sim.Showcase, token, sim.ExpiresAt)
	return err
}

func (s *Store) GetSimulation(ctx context.Context, id string) (store.Simulation, error) {
	const query = `
		select id, seed, drivers, strategy, created_at, completed_at, showcase,
		       coalesce(guest_token, ''), expires_at
		from simulations where id = $1`

	var sim store.Simulation
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&sim.ID, &sim.Seed, &sim.Drivers, &sim.Strategy, &sim.CreatedAt, &sim.CompletedAt,
		&sim.Showcase, &sim.GuestToken, &sim.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Simulation{}, store.ErrNotFound
	}
	if err != nil {
		return store.Simulation{}, err
	}
	return sim, nil
}

func (s *Store) CountSimulationsForToken(ctx context.Context, token string) (int, error) {
	const query = `select count(*) from simulations where guest_token = $1`

	var count int
	if err := s.pool.QueryRow(ctx, query, token).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// PurgeExpired removes anonymous runs and dead sessions in one transaction.
// Events and snapshots go with their simulation through the foreign key
// cascade; a showcase run survives its session expiring because that foreign
// key is on delete set null, not cascade.
func (s *Store) PurgeExpired(ctx context.Context, now time.Time) (store.PurgeResult, error) {
	var result store.PurgeResult

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`delete from simulations where showcase = false and expires_at is not null and expires_at <= $1`, now)
		if err != nil {
			return err
		}
		result.Simulations = int(tag.RowsAffected())

		tag, err = tx.Exec(ctx, `delete from guest_sessions where expires_at <= $1`, now)
		if err != nil {
			return err
		}
		result.Sessions = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return store.PurgeResult{}, err
	}
	return result, nil
}

func (s *Store) MarkShowcase(ctx context.Context, id string, completedAt time.Time) error {
	const query = `update simulations set showcase = true, completed_at = $2 where id = $1`

	tag, err := s.pool.Exec(ctx, query, id, completedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// AppendEvents writes a batch in one round trip. The conflict clause makes a
// retried flush harmless: the log is keyed by (simulation_id, sequence), and
// an event that is already stored is by definition identical to the one being
// written, since events are immutable once emitted.
func (s *Store) AppendEvents(ctx context.Context, events []store.Event) error {
	if len(events) == 0 {
		return nil
	}

	const query = `
		insert into simulation_events (simulation_id, sequence, virtual_time, type, payload, trace_id, recorded_at)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (simulation_id, sequence) do nothing`

	batch := &pgx.Batch{}
	for _, e := range events {
		recordedAt := e.RecordedAt
		if recordedAt.IsZero() {
			recordedAt = time.Now().UTC()
		}
		traceID := any(e.TraceID)
		if e.TraceID == "" {
			traceID = nil
		}
		batch.Queue(query, e.SimulationID, e.Sequence, e.VirtualTime, string(e.Type), e.Payload, traceID, recordedAt)
	}

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range events {
		if _, err := results.Exec(); err != nil {
			return err
		}
	}
	return results.Close()
}

func (s *Store) Events(ctx context.Context, simulationID string, fromSequence, limit int) ([]store.Event, error) {
	const query = `
		select simulation_id, sequence, virtual_time, type, payload, coalesce(trace_id, ''), recorded_at
		from simulation_events
		where simulation_id = $1 and sequence > $2
		order by sequence
		limit $3`

	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, query, simulationID, fromSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]store.Event, 0, limit)
	for rows.Next() {
		var e store.Event
		if err := rows.Scan(&e.SimulationID, &e.Sequence, &e.VirtualTime, &e.Type, &e.Payload, &e.TraceID, &e.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) LatestSequence(ctx context.Context, simulationID string) (int, error) {
	const query = `select coalesce(max(sequence), 0) from simulation_events where simulation_id = $1`

	var sequence int
	if err := s.pool.QueryRow(ctx, query, simulationID).Scan(&sequence); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (s *Store) SaveSnapshot(ctx context.Context, snapshot store.Snapshot) error {
	const query = `
		insert into simulation_snapshots (simulation_id, sequence, virtual_time, payload, recorded_at)
		values ($1, $2, $3, $4, $5)
		on conflict (simulation_id, sequence) do update
		set virtual_time = excluded.virtual_time,
		    payload = excluded.payload,
		    recorded_at = excluded.recorded_at`

	recordedAt := snapshot.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, query, snapshot.SimulationID, snapshot.Sequence, snapshot.VirtualTime, snapshot.Payload, recordedAt)
	return err
}

func (s *Store) SnapshotAtOrBefore(ctx context.Context, simulationID string, sequence int) (store.Snapshot, error) {
	const query = `
		select simulation_id, sequence, virtual_time, payload, recorded_at
		from simulation_snapshots
		where simulation_id = $1 and sequence <= $2
		order by sequence desc
		limit 1`

	var snapshot store.Snapshot
	err := s.pool.QueryRow(ctx, query, simulationID, sequence).Scan(
		&snapshot.SimulationID, &snapshot.Sequence, &snapshot.VirtualTime, &snapshot.Payload, &snapshot.RecordedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Snapshot{}, store.ErrNotFound
	}
	if err != nil {
		return store.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) SaveComparison(ctx context.Context, comparison store.Comparison) error {
	const query = `
		insert into comparisons (id, seed, drivers, result, created_at)
		values ($1, $2, $3, $4, $5)
		on conflict (id) do nothing`

	createdAt := comparison.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, query, comparison.ID, comparison.Seed, comparison.Drivers, comparison.Result, createdAt)
	return err
}

func (s *Store) GetComparison(ctx context.Context, id string) (store.Comparison, error) {
	const query = `select id, seed, drivers, result, created_at from comparisons where id = $1`

	var c store.Comparison
	err := s.pool.QueryRow(ctx, query, id).Scan(&c.ID, &c.Seed, &c.Drivers, &c.Result, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Comparison{}, store.ErrNotFound
	}
	if err != nil {
		return store.Comparison{}, err
	}
	return c, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) Close() error {
	s.pool.Close()
	return nil
}
