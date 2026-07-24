package service

import (
	"errors"
	"testing"
	"time"

	"dispatchlab/internal/domain"
)

func TestCreateAndGet(t *testing.T) {
	m := NewManager(0)
	id, err := m.Create("", 1, 4)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == "" {
		t.Fatal("expected a generated id")
	}
	if _, ok := m.Get(id); !ok {
		t.Fatal("expected the created simulation to be retrievable")
	}
}

func TestCreateIsIdempotentForExplicitID(t *testing.T) {
	m := NewManager(0)
	first, err := m.Create("fixed", 1, 4)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := m.Create("fixed", 1, 4)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first != second || first != "fixed" {
		t.Fatalf("expected repeated create with the same id to be idempotent, got %s then %s", first, second)
	}
}

func TestCreateRespectsCapacity(t *testing.T) {
	m := NewManager(1)
	if _, err := m.Create("a", 1, 2); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := m.Create("b", 1, 2); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity, got %v", err)
	}
}

func TestGetMissingReturnsFalse(t *testing.T) {
	m := NewManager(0)
	if _, ok := m.Get("does-not-exist"); ok {
		t.Fatal("expected missing simulation to report not found")
	}
}

func TestCommandsOnMissingSimulationReturnErrNotFound(t *testing.T) {
	m := NewManager(0)
	if err := m.PlaceOrder("missing", "a", "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PlaceOrder: expected ErrNotFound, got %v", err)
	}
	if err := m.SetPaused("missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPaused: expected ErrNotFound, got %v", err)
	}
	if err := m.Reset("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reset: expected ErrNotFound, got %v", err)
	}
	if err := m.SetSpeed("missing", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetSpeed: expected ErrNotFound, got %v", err)
	}
	if _, err := m.Snapshot("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Snapshot: expected ErrNotFound, got %v", err)
	}
}

func TestSnapshotReflectsPlacedOrder(t *testing.T) {
	m := NewManager(0)
	id, _ := m.Create("", 5, 4)
	defer m.Shutdown()

	sim, _ := m.Get(id)
	nodeIDs := sortedNodeIDs(t, sim)

	if err := m.PlaceOrder(id, nodeIDs[0], nodeIDs[len(nodeIDs)-1]); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	// the command is processed asynchronously on the simulation's own
	// goroutine, so poll briefly for the driver to leave idle.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := m.Snapshot(id)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if anyDriverBusy(t, snap) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected a driver to be assigned after placing an order")
}

func TestStreamLookupResolvesCreatedSimulation(t *testing.T) {
	m := NewManager(0)
	id, _ := m.Create("", 1, 2)
	defer m.Shutdown()

	hub, snap, ok := m.StreamLookup(id)
	if !ok || hub == nil || snap == nil {
		t.Fatal("expected StreamLookup to resolve hub and snapshotter for a live simulation")
	}
	if _, _, ok := m.StreamLookup("missing"); ok {
		t.Fatal("expected StreamLookup to report not found for an unknown id")
	}
}

func sortedNodeIDs(t *testing.T, sim interface{ CurrentSnapshot() domain.Event }) []domain.NodeID {
	t.Helper()
	snap := sim.CurrentSnapshot()
	payload := snap.Payload.(map[string]any)
	nodes := payload["nodes"].([]map[string]any)
	ids := make([]domain.NodeID, len(nodes))
	for i, n := range nodes {
		ids[i] = n["id"].(domain.NodeID)
	}
	return ids
}

func anyDriverBusy(t *testing.T, snap domain.Event) bool {
	t.Helper()
	payload := snap.Payload.(map[string]any)
	for _, d := range payload["drivers"].([]map[string]any) {
		if d["status"].(domain.DriverStatus) != domain.DriverIdle {
			return true
		}
	}
	return false
}
