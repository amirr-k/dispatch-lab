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
)

// shutdownFlushTimeout bounds how long shutdown waits for recorders to write
// their last batch before giving up on it.
const shutdownFlushTimeout = 5 * time.Second

// entry bundles a running simulation with its fanout hub, recorder, and
// cancel handle.
type entry struct {
	sim      *simulation.Simulation
	hub      *ws.Hub
	recorder *replay.Recorder
	cancel   context.CancelFunc
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

// Create starts a new simulation and its hub. An empty id is replaced with a
// generated one. It returns the simulation's id.
func (m *Manager) Create(id string, seed int64, drivers int) (string, error) {
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
	ctx, cancel := context.WithCancel(context.Background())

	m.recordSimulation(ctx, id, seed, drivers)

	// the recorder sits between the simulation and the hub so every event is
	// persisted exactly once, rather than being one more subscriber the hub
	// would drop events for under load.
	recorder := replay.NewRecorder(id, replay.RecorderConfig{
		Store:       m.cfg.Store,
		Snapshotter: sim,
		Metrics:     m.cfg.Metrics,
		Logger:      m.cfg.Logger,
	})

	go sim.Run(ctx)
	hub := ws.NewHubWithMetrics(recorder.Tap(ctx, sim.Events()), m.cfg.Metrics)

	m.entries[id] = &entry{sim: sim, hub: hub, recorder: recorder, cancel: cancel}
	m.cfg.Metrics.ActiveSimulations().Set(float64(len(m.entries)))
	m.cfg.Logger.Info("created simulation", "simulation_id", id, "seed", seed, "drivers", drivers)
	return id, nil
}

// recordSimulation writes the metadata row a replay needs to name its run.
// A failure is logged and counted, never fatal: a demo that cannot reach its
// database should still run, just without a durable replay.
func (m *Manager) recordSimulation(ctx context.Context, id string, seed int64, drivers int) {
	if m.cfg.Store == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	err := m.cfg.Store.CreateSimulation(writeCtx, store.Simulation{
		ID:        id,
		Seed:      seed,
		Drivers:   drivers,
		Strategy:  string(simulation.StrategyBaseline),
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
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

// StreamLookup resolves the fanout hub and snapshot source for a simulation.
// It is the seam the WebSocket handler uses without importing this package's
// concrete types.
func (m *Manager) StreamLookup(id string) (*ws.Hub, ws.Snapshotter, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, nil, false
	}
	return e.hub, e.sim, true
}

// PlaceOrder submits an order to a simulation.
func (m *Manager) PlaceOrder(ctx context.Context, id string, pickup, destination domain.NodeID) error {
	return m.submit(ctx, id, simulation.PlaceOrder{Pickup: pickup, Destination: destination})
}

// SetPaused pauses or resumes a simulation.
func (m *Manager) SetPaused(ctx context.Context, id string, paused bool) error {
	return m.submit(ctx, id, simulation.SetPaused{Paused: paused})
}

// Reset returns a simulation to its initial seeded state.
func (m *Manager) Reset(ctx context.Context, id string) error {
	return m.submit(ctx, id, simulation.Reset{})
}

// SetSpeed changes a simulation's live playback rate.
func (m *Manager) SetSpeed(ctx context.Context, id string, multiplier float64) error {
	return m.submit(ctx, id, simulation.SetSpeed{Multiplier: multiplier})
}

// CloseRoad closes a road segment in a simulation.
func (m *Manager) CloseRoad(ctx context.Context, id string, edgeID domain.EdgeID) error {
	return m.submit(ctx, id, simulation.CloseRoad{EdgeID: edgeID})
}

// Snapshot returns a current-state snapshot event for a simulation.
func (m *Manager) Snapshot(id string) (domain.Event, error) {
	sim, ok := m.Get(id)
	if !ok {
		return domain.Event{}, ErrNotFound
	}
	return sim.CurrentSnapshot(), nil
}

// MarkShowcase retains a run permanently so its replay URL keeps working. It
// flushes a final snapshot first, so the stored history ends on the state the
// visitor actually saw. A run that was never persisted cannot be showcased.
func (m *Manager) MarkShowcase(ctx context.Context, id string) error {
	if m.cfg.Store == nil {
		return ErrNotFound
	}

	sim, live := m.Get(id)
	if live {
		m.writeFinalSnapshot(ctx, id, sim)
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
	sim, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}
	if !sim.TrySubmitCtx(ctx, cmd) {
		return ErrBusy
	}
	return nil
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
