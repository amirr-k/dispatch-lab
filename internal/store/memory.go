package store

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Store for tests and for local development with no
// database attached — a run is then simply not durable, and its replay URL
// only lives as long as the process. It retains everything it is given for
// the life of the process; the deployed path is the Postgres store.
type Memory struct {
	mu          sync.RWMutex
	simulations map[string]Simulation
	events      map[string][]Event
	seen        map[string]map[int]bool
	snapshots   map[string][]Snapshot
	comparisons map[string]Comparison
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		simulations: make(map[string]Simulation),
		events:      make(map[string][]Event),
		seen:        make(map[string]map[int]bool),
		snapshots:   make(map[string][]Snapshot),
		comparisons: make(map[string]Comparison),
	}
}

func (m *Memory) CreateSimulation(_ context.Context, sim Simulation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.simulations[sim.ID]; exists {
		return nil
	}
	m.simulations[sim.ID] = sim
	return nil
}

func (m *Memory) GetSimulation(_ context.Context, id string) (Simulation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sim, ok := m.simulations[id]
	if !ok {
		return Simulation{}, ErrNotFound
	}
	return sim, nil
}

func (m *Memory) MarkShowcase(_ context.Context, id string, completedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sim, ok := m.simulations[id]
	if !ok {
		return ErrNotFound
	}
	sim.Showcase = true
	sim.CompletedAt = &completedAt
	m.simulations[id] = sim
	return nil
}

// AppendEvents ignores an event whose sequence was already written, matching
// the Postgres store's conflict handling: the event log is append-only and
// keyed by (simulation, sequence), so a retried batch must not duplicate.
func (m *Memory) AppendEvents(_ context.Context, events []Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	touched := make(map[string]bool)
	for _, e := range events {
		seen := m.seen[e.SimulationID]
		if seen == nil {
			seen = make(map[int]bool)
			m.seen[e.SimulationID] = seen
		}
		if seen[e.Sequence] {
			continue
		}
		seen[e.Sequence] = true
		m.events[e.SimulationID] = append(m.events[e.SimulationID], e)
		touched[e.SimulationID] = true
	}
	for id := range touched {
		list := m.events[id]
		sort.Slice(list, func(i, j int) bool { return list[i].Sequence < list[j].Sequence })
	}
	return nil
}

func (m *Memory) Events(_ context.Context, simulationID string, fromSequence, limit int) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Event, 0, limit)
	for _, e := range m.events[simulationID] {
		if e.Sequence <= fromSequence {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *Memory) LatestSequence(_ context.Context, simulationID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	events := m.events[simulationID]
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].Sequence, nil
}

func (m *Memory) SaveSnapshot(_ context.Context, snapshot Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.snapshots[snapshot.SimulationID]
	for i, existing := range list {
		if existing.Sequence == snapshot.Sequence {
			list[i] = snapshot
			return nil
		}
	}
	list = append(list, snapshot)
	sort.Slice(list, func(i, j int) bool { return list[i].Sequence < list[j].Sequence })
	m.snapshots[snapshot.SimulationID] = list
	return nil
}

func (m *Memory) SnapshotAtOrBefore(_ context.Context, simulationID string, sequence int) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var best Snapshot
	found := false
	for _, s := range m.snapshots[simulationID] {
		if s.Sequence <= sequence {
			best, found = s, true
			continue
		}
		break
	}
	if !found {
		return Snapshot{}, ErrNotFound
	}
	return best, nil
}

func (m *Memory) SaveComparison(_ context.Context, comparison Comparison) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.comparisons[comparison.ID] = comparison
	return nil
}

func (m *Memory) GetComparison(_ context.Context, id string) (Comparison, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.comparisons[id]
	if !ok {
		return Comparison{}, ErrNotFound
	}
	return c, nil
}

func (m *Memory) Close() error { return nil }
