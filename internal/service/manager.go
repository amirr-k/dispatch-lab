// Package service owns the set of live simulations and turns application
// commands and queries into actions on the right simulation goroutine.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/simulation"
	"dispatchlab/internal/transport/ws"
)

var (
	ErrNotFound = errors.New("simulation not found")
	ErrCapacity = errors.New("simulation capacity reached")
	ErrBusy     = errors.New("simulation command buffer full")
)

// entry bundles a running simulation with its fanout hub and cancel handle.
type entry struct {
	sim    *simulation.Simulation
	hub    *ws.Hub
	cancel context.CancelFunc
}

// Manager creates, tracks, and routes commands to simulations. Every method
// is safe for concurrent use.
type Manager struct {
	mu      sync.Mutex
	entries map[string]*entry
	max     int
}

// NewManager returns a manager that will hold at most max concurrent
// simulations. A non-positive max means unlimited.
func NewManager(max int) *Manager {
	return &Manager{entries: make(map[string]*entry), max: max}
}

// Create starts a new simulation and its hub. An empty id is replaced with a
// generated one. It returns the simulation's id.
func (m *Manager) Create(id string, seed int64, drivers int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.max > 0 && len(m.entries) >= m.max {
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

	sim := simulation.New(id, seed, drivers)
	ctx, cancel := context.WithCancel(context.Background())
	go sim.Run(ctx)
	hub := ws.NewHub(sim.Events())

	m.entries[id] = &entry{sim: sim, hub: hub, cancel: cancel}
	return id, nil
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
func (m *Manager) PlaceOrder(id string, pickup, destination domain.NodeID) error {
	return m.submit(id, simulation.PlaceOrder{Pickup: pickup, Destination: destination})
}

// SetPaused pauses or resumes a simulation.
func (m *Manager) SetPaused(id string, paused bool) error {
	return m.submit(id, simulation.SetPaused{Paused: paused})
}

// Reset returns a simulation to its initial seeded state.
func (m *Manager) Reset(id string) error {
	return m.submit(id, simulation.Reset{})
}

// SetSpeed changes a simulation's live playback rate.
func (m *Manager) SetSpeed(id string, multiplier float64) error {
	return m.submit(id, simulation.SetSpeed{Multiplier: multiplier})
}

// Snapshot returns a current-state snapshot event for a simulation.
func (m *Manager) Snapshot(id string) (domain.Event, error) {
	sim, ok := m.Get(id)
	if !ok {
		return domain.Event{}, ErrNotFound
	}
	return sim.CurrentSnapshot(), nil
}

// Shutdown cancels every running simulation.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		e.cancel()
	}
}

func (m *Manager) submit(id string, cmd simulation.Command) error {
	sim, ok := m.Get(id)
	if !ok {
		return ErrNotFound
	}
	if !sim.TrySubmit(cmd) {
		return ErrBusy
	}
	return nil
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
