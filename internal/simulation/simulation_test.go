package simulation

import (
	"reflect"
	"testing"

	"dispatchlab/internal/domain"
)

const (
	scenarioSeed    = 7
	scenarioDrivers = 5
	scenarioPickup  = domain.NodeID("n-0-5")
	scenarioDest    = domain.NodeID("n-5-0")
)

// runScenario drives a fixed command/tick script headlessly and returns the
// full event sequence. No wall-clock time is involved, so it is reproducible.
func runScenario(seed int64) []domain.Event {
	s := New("sim-golden", seed, scenarioDrivers)
	var evs []domain.Event
	evs = append(evs, s.Start()...)
	evs = append(evs, s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})...)
	for i := 0; i < 60; i++ {
		evs = append(evs, s.Advance()...)
	}
	return evs
}

// normalize strips the one measured, wall-clock-derived field from the event
// stream (assignment compute latency) so the rest can be compared for exact
// determinism. That latency is an observability metric, not simulation state.
func normalize(evs []domain.Event) []domain.Event {
	out := make([]domain.Event, len(evs))
	for i, e := range evs {
		if e.Type == domain.EventOrderAssigned {
			if m, ok := e.Payload.(map[string]any); ok {
				cp := make(map[string]any, len(m))
				for k, v := range m {
					if k != "assignmentComputeMs" {
						cp[k] = v
					}
				}
				e.Payload = cp
			}
		}
		out[i] = e
	}
	return out
}

// TestDeterministicReplay is the Phase 1 exit gate: the same seed and the same
// commands must produce the same event sequence.
func TestDeterministicReplay(t *testing.T) {
	a := runScenario(scenarioSeed)
	b := runScenario(scenarioSeed)

	if len(a) == 0 {
		t.Fatal("scenario produced no events")
	}
	if !reflect.DeepEqual(normalize(a), normalize(b)) {
		t.Fatal("same seed and commands produced different event sequences")
	}
	assertSequential(t, a)

	assigned := false
	for _, e := range a {
		if e.Type == domain.EventOrderAssigned {
			assigned = true
		}
	}
	if !assigned {
		t.Fatal("expected the scenario to assign the order")
	}
}

func TestPlaceOrderEmitsAssignmentSequence(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	evs := s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	wantOrder := []domain.EventType{
		domain.EventOrderPlaced,
		domain.EventRouteComputed,
		domain.EventOrderAssigned,
		domain.EventDriverStatusChanged,
	}
	if len(evs) != len(wantOrder) {
		t.Fatalf("expected %d events, got %d: %+v", len(wantOrder), len(evs), types(evs))
	}
	for i, want := range wantOrder {
		if evs[i].Type != want {
			t.Fatalf("event %d: want %s got %s", i, want, evs[i].Type)
		}
	}
}

func TestPlaceOrderUnassignableWhenNoDriverIdle(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	for _, d := range s.drivers {
		d.Status = domain.DriverUnavailable
	}

	evs := s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	if len(evs) != 2 || evs[0].Type != domain.EventOrderPlaced || evs[1].Type != domain.EventOrderUnassignable {
		t.Fatalf("expected placed+unassignable, got %v", types(evs))
	}
}

func TestPausedSimulationHoldsState(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	evs := s.Apply(SetPaused{Paused: true})
	if len(evs) != 1 || evs[0].Type != domain.EventSimulationPaused {
		t.Fatalf("expected a single simulation.paused event, got %v", types(evs))
	}
	if m, ok := evs[0].Payload.(map[string]any); !ok || m["paused"] != true {
		t.Fatalf("expected paused=true in payload, got %+v", evs[0].Payload)
	}

	before := s.virtualTime
	for i := 0; i < 10; i++ {
		if evs := s.Advance(); len(evs) != 0 {
			t.Fatalf("expected no events while paused, got %v", types(evs))
		}
	}
	if s.virtualTime != before {
		t.Fatalf("expected virtual time to hold while paused, went from %v to %v", before, s.virtualTime)
	}

	resumeEvs := s.Apply(SetPaused{Paused: false})
	if len(resumeEvs) != 1 || resumeEvs[0].Type != domain.EventSimulationPaused {
		t.Fatalf("expected a single simulation.paused event on resume, got %v", types(resumeEvs))
	}
	if evs := s.Advance(); len(evs) == 0 {
		t.Fatal("expected ticks to resume producing events after unpausing")
	}
}

func TestSnapshotReflectsPausedAndSpeed(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	evs := s.Start()
	payload := evs[0].Payload.(map[string]any)
	if payload["paused"] != false || payload["speed"] != 1.0 {
		t.Fatalf("expected fresh snapshot paused=false speed=1, got %+v", payload)
	}

	s.Apply(SetPaused{Paused: true})
	s.Apply(SetSpeed{Multiplier: 4})

	snap := s.buildSnapshotEvent()
	got := snap.Payload.(map[string]any)
	if got["paused"] != true || got["speed"] != 4.0 {
		t.Fatalf("expected snapshot to reflect paused=true speed=4, got %+v", got)
	}
}

func TestResetRestoresInitialState(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	for i := 0; i < 5; i++ {
		s.Advance()
	}

	evs := s.Apply(Reset{})
	if len(evs) != 1 || evs[0].Type != domain.EventSimulationSnapshot {
		t.Fatalf("expected reset to emit a fresh snapshot, got %v", types(evs))
	}
	if len(s.orders) != 0 {
		t.Fatalf("expected orders cleared after reset, got %d", len(s.orders))
	}
	for _, d := range s.drivers {
		if d.Status != domain.DriverIdle {
			t.Fatalf("expected driver %s idle after reset, got %s", d.ID, d.Status)
		}
	}
	if s.virtualTime != 0 {
		t.Fatalf("expected virtual time reset to 0, got %v", s.virtualTime)
	}
}

func TestDriverMovesAndDelivers(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	sawMove, sawDelivering, sawDelivered := false, false, false
	for i := 0; i < 300 && !sawDelivered; i++ {
		for _, e := range s.Advance() {
			switch e.Type {
			case domain.EventDriverPositionUpdate:
				sawMove = true
			case domain.EventDriverStatusChanged:
				if m, ok := e.Payload.(map[string]any); ok && m["status"] == domain.DriverDelivering {
					sawDelivering = true
				}
			case domain.EventOrderDelivered:
				sawDelivered = true
			}
		}
		checkInvariants(t, s)
	}

	if !sawMove {
		t.Fatal("expected driver position updates")
	}
	if !sawDelivering {
		t.Fatal("expected the driver to reach pickup and switch to delivering")
	}
	if !sawDelivered {
		t.Fatal("expected the order to be delivered")
	}
	for _, d := range s.drivers {
		if d.Status != domain.DriverIdle {
			t.Fatalf("driver %s should be idle after delivery, got %s", d.ID, d.Status)
		}
	}
}

// checkInvariants asserts the structural rules that must hold after any step:
// a driver on a route sits exactly on its current route node.
func checkInvariants(t *testing.T, s *Simulation) {
	t.Helper()
	for _, d := range s.drivers {
		if len(d.Route) == 0 {
			continue
		}
		if d.RouteIndex < 0 || d.RouteIndex >= len(d.Route) {
			t.Fatalf("driver %s route index %d out of bounds (len %d)", d.ID, d.RouteIndex, len(d.Route))
		}
		if d.Position != d.Route[d.RouteIndex] {
			t.Fatalf("driver %s position %s != route node %s", d.ID, d.Position, d.Route[d.RouteIndex])
		}
	}
}

func assertSequential(t *testing.T, evs []domain.Event) {
	t.Helper()
	for i, e := range evs {
		if e.Sequence != i+1 {
			t.Fatalf("event %d has non-sequential sequence %d", i, e.Sequence)
		}
	}
}

func types(evs []domain.Event) []domain.EventType {
	out := make([]domain.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}
