// Package service owns the set of live simulations and turns application
// commands and queries into actions on the right simulation goroutine.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/replay"
	"dispatchlab/internal/simulation"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
	"dispatchlab/internal/transport/ws"
)

var (
	ErrNotFound = errors.New("simulation not found")
	ErrCapacity = errors.New("simulation capacity reached")
	ErrBusy     = errors.New("simulation command buffer full")
	// ErrOrderLimit means this run already holds as many orders as it may.
	ErrOrderLimit = errors.New("simulation order limit reached")
)

const (
	// shutdownFlushTimeout bounds how long shutdown waits for recorders to
	// write their last batch before giving up on it.
	shutdownFlushTimeout = 5 * time.Second
	// MaxOrdersPerRun bounds how many orders one simulation will accept, so
	// a visitor holding down the mouse cannot grow a run's state without
	// limit. Resetting a run clears the count along with the orders.
	MaxOrdersPerRun = 200
)

// tokenKey carries the requesting session's identity on the context. Putting
// it there rather than in every method signature means the ownership check
// lives in one place — submit — and a new command route cannot accidentally
// skip it.
type tokenKey struct{}

// WithToken tags a context with the guest token making the request.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// TokenFrom returns the guest token on a context, empty if there is none.
func TokenFrom(ctx context.Context) string {
	token, _ := ctx.Value(tokenKey{}).(string)
	return token
}

// entry bundles a running simulation with its fanout hub, recorder, and
// cancel handle.
type entry struct {
	sim      *simulation.Simulation
	hub      *ws.Hub
	recorder *replay.Recorder
	cancel   context.CancelFunc
	// owner is the guest token that created this run. Empty means the server
	// created it — the seeded showcase runs — which are public.
	owner string
	// orders counts what has been placed, so one run cannot grow without
	// bound.
	orders int
}

// ManagerConfig configures a Manager. Every field is optional: with no store
// a simulation simply is not persisted, and with no metrics or logger nothing
// is recorded about it.
type ManagerConfig struct {
	// Max bounds concurrent simulations. Non-positive means unlimited.
	Max     int
	Store   store.Store
	Metrics *telemetry.Metrics
	Logger  *slog.Logger
	// Sessions enforces per-session quotas and supplies the retention window
	// for anonymous runs. Nil disables both.
	Sessions *Sessions
}

// Manager creates, tracks, and routes commands to simulations. Every method
// is safe for concurrent use.
type Manager struct {
	mu      sync.Mutex
	entries map[string]*entry
	cfg     ManagerConfig
}

// NewManager returns a manager that will hold at most max concurrent
// simulations, with no persistence or telemetry attached.
func NewManager(max int) *Manager {
	return NewManagerWithConfig(ManagerConfig{Max: max})
}

// NewManagerWithConfig returns a manager wired to a store and telemetry.
func NewManagerWithConfig(cfg ManagerConfig) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Manager{entries: make(map[string]*entry), cfg: cfg}
}

// Create starts a new simulation and its hub, owned by whichever session the
// context identifies. An empty id is replaced with a generated one. It
// returns the simulation's id.
func (m *Manager) Create(ctx context.Context, id string, seed int64, drivers int) (string, error) {
	owner := TokenFrom(ctx)

	if err := m.checkQuota(ctx, owner); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg.Max > 0 && len(m.entries) >= m.cfg.Max {
		if _, exists := m.entries[id]; !exists {
			return "", ErrCapacity
		}
	}
	if id == "" {
		id = generateID()
	}
	if _, exists := m.entries[id]; exists {
		return id, nil
	}

	sim := simulation.NewWithConfig(simulation.Config{
		ID:          id,
		Seed:        seed,
		DriverCount: drivers,
		Metrics:     m.cfg.Metrics,
		Logger:      m.cfg.Logger,
	})
	runCtx, cancel := context.WithCancel(context.Background())

	m.recordSimulation(runCtx, id, seed, drivers, owner)

	// the recorder sits between the simulation and the hub so every event is
	// persisted exactly once, rather than being one more subscriber the hub
	// would drop events for under load.
	recorder := replay.NewRecorder(id, replay.RecorderConfig{
		Store:       m.cfg.Store,
		Snapshotter: sim,
		Metrics:     m.cfg.Metrics,
		Logger:      m.cfg.Logger,
	})

	go sim.Run(runCtx)
	hub := ws.NewHubWithMetrics(recorder.Tap(runCtx, sim.Events()), m.cfg.Metrics)

	m.entries[id] = &entry{sim: sim, hub: hub, recorder: recorder, cancel: cancel, owner: owner}
	m.cfg.Metrics.ActiveSimulations().Set(float64(len(m.entries)))
	m.cfg.Logger.Info("created simulation", "simulation_id", id, "seed", seed, "drivers", drivers)
	return id, nil
}

// checkQuota refuses a session that already holds its allowance of runs.
func (m *Manager) checkQuota(ctx context.Context, owner string) error {
	if m.cfg.Sessions == nil || owner == "" {
		return nil
	}

	m.mu.Lock()
	held := 0
	for _, e := range m.entries {
		if e.owner == owner {
			held++
		}
	}
	m.mu.Unlock()

	return m.cfg.Sessions.CheckQuota(ctx, owner, held)
}

// recordSimulation writes the metadata row a replay needs to name its run.
// A failure is logged and counted, never fatal: a demo that cannot reach its
// database should still run, just without a durable replay.
func (m *Manager) recordSimulation(ctx context.Context, id string, seed int64, drivers int, owner string) {
	if m.cfg.Store == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	sim := store.Simulation{
		ID:         id,
		Seed:       seed,
		Drivers:    drivers,
		Strategy:   string(simulation.StrategyBaseline),
		CreatedAt:  time.Now().UTC(),
		GuestToken: owner,
	}
	// an anonymous run is kept only for a while; marking it a showcase is
	// what makes it permanent.
	if m.cfg.Sessions != nil && owner != "" {
		expiresAt := time.Now().UTC().Add(m.cfg.Sessions.RunTTL())
		sim.ExpiresAt = &expiresAt
	}

	if err := m.cfg.Store.CreateSimulation(writeCtx, sim); err != nil {
		m.cfg.Metrics.PersistenceErrors().Inc()
		m.cfg.Logger.Error("could not record a simulation", "simulation_id", id, "error", err)
	}
}

// Get returns the simulation with the given id.
func (m *Manager) Get(id string) (*simulation.Simulation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, false
	}
	return e.sim, true
}

// StreamLookup resolves the fanout hub and snapshot source for a simulation,
// for the session identified by token. It is the seam the WebSocket handler
// uses without importing this package's concrete types, and it applies the
// same ownership rule the command routes do — a stream is exactly as private
// as the run behind it.
func (m *Manager) StreamLookup(id, token string) (*ws.Hub, ws.Snapshotter, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, nil, false
	}
	if authorize(e, token) != nil {
		return nil, nil, false
	}
	return e.hub, e.sim, true
}

// PlaceOrder submits an order to a simulation, refusing once the run holds
// as many as it may.
func (m *Manager) PlaceOrder(ctx context.Context, id string, pickup, destination domain.NodeID) error {
	if err := m.reserveOrder(ctx, id); err != nil {
		return err
	}
	if err := m.submit(ctx, id, simulation.PlaceOrder{Pickup: pickup, Destination: destination}); err != nil {
		m.releaseOrder(id)
		return err
	}
	return nil
}

// reserveOrder claims one of a run's order slots before the command is
// submitted, so concurrent requests cannot both squeeze past the limit.
func (m *Manager) reserveOrder(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[id]
	if !ok {
		return ErrNotFound
	}
	if err := authorize(e, TokenFrom(ctx)); err != nil {
		return err
	}
	if e.orders >= MaxOrdersPerRun {
		return ErrOrderLimit
	}
	e.orders++
	return nil
}

func (m *Manager) releaseOrder(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[id]; ok && e.orders > 0 {
		e.orders--
	}
}

// SetPaused pauses or resumes a simulation.
func (m *Manager) SetPaused(ctx context.Context, id string, paused bool) error {
	return m.submit(ctx, id, simulation.SetPaused{Paused: paused})
}

// Reset returns a simulation to its initial seeded state, which clears its
// orders and therefore its order count.
func (m *Manager) Reset(ctx context.Context, id string) error {
	if err := m.submit(ctx, id, simulation.Reset{}); err != nil {
		return err
	}
	m.mu.Lock()
	if e, ok := m.entries[id]; ok {
		e.orders = 0
	}
	m.mu.Unlock()
	return nil
}

// SetSpeed changes a simulation's live playback rate.
func (m *Manager) SetSpeed(ctx context.Context, id string, multiplier float64) error {
	return m.submit(ctx, id, simulation.SetSpeed{Multiplier: multiplier})
}

// CloseRoad closes a road segment in a simulation. It returns the command id
// assigned to the closure, which is stamped as causationId on the resulting
// road.closed event and any route.invalidated/route.computed/
// order.unassignable events the reroutes it triggers produce, so a caller
// can determine exactly which events this specific closure caused.
func (m *Manager) CloseRoad(ctx context.Context, id string, edgeID domain.EdgeID) (string, error) {
	commandID := generateID()
	if err := m.submit(ctx, id, simulation.CloseRoad{EdgeID: edgeID, CommandID: commandID}); err != nil {
		return "", err
	}
	return commandID, nil
}

// ReopenRoad reopens a previously closed road segment for the simulation the
// session on ctx may see.
func (m *Manager) ReopenRoad(ctx context.Context, id string, edgeID domain.EdgeID) error {
	return m.submit(ctx, id, simulation.ReopenRoad{EdgeID: edgeID})
}

// Snapshot returns a current-state snapshot event for a simulation the
// session on ctx may see.
func (m *Manager) Snapshot(ctx context.Context, id string) (domain.Event, error) {
	if err := m.Authorize(ctx, id); err != nil {
		return domain.Event{}, err
	}
	sim, ok := m.Get(id)
	if !ok {
		return domain.Event{}, ErrNotFound
	}
	return sim.CurrentSnapshot(), nil
}

// MarkShowcase retains a run permanently so its replay URL keeps working. For
// a still-running simulation it writes a final snapshot and forces the
// recorder to flush, so the stored history ends on the state the visitor
// actually saw - the recorder otherwise only writes on its own batch/interval
// schedule, and without a forced flush here a replay opened immediately after
// saving could be missing whatever hadn't reached it yet. A completed run
// needs neither: its recorder already flushed everything on the way out.
func (m *Manager) MarkShowcase(ctx context.Context, id string) error {
	if m.cfg.Store == nil {
		return ErrNotFound
	}

	m.mu.Lock()
	e, live := m.entries[id]
	m.mu.Unlock()

	if live {
		if err := m.Authorize(ctx, id); err != nil {
			return err
		}
		m.writeFinalSnapshot(ctx, id, e.sim)

		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := e.recorder.Flush(flushCtx); err != nil {
			m.cfg.Logger.Error("could not flush pending events before showcasing",
				"simulation_id", id, "error", err)
		}
	}

	err := m.cfg.Store.MarkShowcase(ctx, id, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		m.cfg.Metrics.PersistenceErrors().Inc()
		return err
	}
	m.cfg.Logger.Info("marked a run as a showcase", "simulation_id", id)
	return nil
}

func (m *Manager) writeFinalSnapshot(ctx context.Context, id string, sim *simulation.Simulation) {
	event := sim.CurrentSnapshot()
	record, err := store.EventFrom(event, "", time.Now().UTC())
	if err != nil {
		m.cfg.Metrics.PersistenceErrors().Inc()
		m.cfg.Logger.Error("could not encode a final snapshot", "simulation_id", id, "error", err)
		return
	}
	snapshot := store.Snapshot{
		SimulationID: id,
		Sequence:     event.Sequence,
		VirtualTime:  event.VirtualTime,
		Payload:      record.Payload,
		RecordedAt:   time.Now().UTC(),
	}
	if err := m.cfg.Store.SaveSnapshot(ctx, snapshot); err != nil {
		m.cfg.Metrics.PersistenceErrors().Inc()
		m.cfg.Logger.Error("could not persist a final snapshot", "simulation_id", id, "error", err)
		return
	}
	m.cfg.Metrics.SnapshotsWritten().Inc()
}

// Shutdown cancels every running simulation and waits for their recorders to
// flush, so a clean stop does not lose the tail of an event log.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.entries = make(map[string]*entry)
	m.mu.Unlock()

	for _, e := range entries {
		e.cancel()
	}

	deadline := time.After(shutdownFlushTimeout)
	for _, e := range entries {
		select {
		case <-e.recorder.Done():
		case <-deadline:
			m.cfg.Logger.Warn("gave up waiting for a recorder to flush")
			m.cfg.Metrics.ActiveSimulations().Set(0)
			return
		}
	}
	m.cfg.Metrics.ActiveSimulations().Set(0)
}

func (m *Manager) submit(ctx context.Context, id string, cmd simulation.Command) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if err := authorize(e, TokenFrom(ctx)); err != nil {
		return err
	}
	if !e.sim.TrySubmitCtx(ctx, cmd) {
		return ErrBusy
	}
	return nil
}

// authorize enforces that a run is only reachable by the session that made
// it. A run with no owner was created by the server itself — the seeded
// showcase runs — and is public.
func authorize(e *entry, token string) error {
	if e.owner == "" || e.owner == token {
		return nil
	}
	// reported as not-found rather than forbidden: a visitor should not be
	// able to learn that someone else's simulation id exists.
	return ErrNotFound
}

// Authorize reports whether the session on ctx may act on a simulation. The
// HTTP layer uses it for reads, which do not go through submit.
func (m *Manager) Authorize(ctx context.Context, id string) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	return authorize(e, TokenFrom(ctx))
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
