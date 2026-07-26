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

// TestDeterministicReplay is the Phase 1 exit gate: the same seed and the same
// commands must produce the same event sequence.
func TestDeterministicReplay(t *testing.T) {
	a := runScenario(scenarioSeed)
	b := runScenario(scenarioSeed)

	if len(a) == 0 {
		t.Fatal("scenario produced no events")
	}
	if !reflect.DeepEqual(a, b) {
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

// baseline used to reject an order the instant no driver was idle, with no
// way to reconsider it later - a different admission policy than optimized
// matching's queue-and-retry, which meant a comparison between the two could
// never attribute a difference to matching alone. An order is now only ever
// rejected outright when a genuinely idle driver could not reach it (a real
// dead end, covered by TestPlaceOrderUnassignableWhenGenuinelyUnreachable);
// "everyone is busy right now" queues.
func TestPlaceOrderQueuesRatherThanRejectsWhenAllDriversBusy(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	for _, d := range s.drivers {
		d.Status = domain.DriverDelivering
	}

	evs := s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	if len(evs) != 1 || evs[0].Type != domain.EventOrderPlaced {
		t.Fatalf("expected only order.placed while every driver is busy, got %v", types(evs))
	}
	if len(s.pendingOrders) != 1 {
		t.Fatalf("expected the order to be queued, got pendingOrders=%v", s.pendingOrders)
	}

	// freeing a driver and re-running the tick's retry path should now place it.
	var freed domain.DriverID
	for id := range s.drivers {
		freed = id
		break
	}
	s.drivers[freed].Status = domain.DriverIdle
	s.retryPendingBaseline()

	if len(s.pendingOrders) != 0 {
		t.Fatalf("expected the queue to drain once a driver freed up, got %v", s.pendingOrders)
	}
}

// TestPlaceOrderUnassignableWhenGenuinelyUnreachable uses a minimal 3-node
// line (a-b-c) with an isolated pickup so there is truly no path - the one
// case where an immediate, permanent rejection is correct instead of a queue.
func TestPlaceOrderUnassignableWhenGenuinelyUnreachable(t *testing.T) {
	city := &domain.City{
		Nodes: map[domain.NodeID]domain.Node{
			"a": {ID: "a", X: 0, Y: 0},
			"b": {ID: "b", X: 1, Y: 0},
		},
		Edges: map[domain.NodeID][]domain.Edge{
			"a": nil,
			"b": nil,
		},
	}
	drivers := map[domain.DriverID]*domain.Driver{
		"d1": {ID: "d1", Position: "a", Status: domain.DriverIdle},
	}
	s := &Simulation{
		ID:       "unreachable-test",
		City:     city,
		drivers:  drivers,
		orders:   make(map[domain.OrderID]*domain.Order),
		commands: make(chan envelope, 8),
		events:   make(chan domain.Event, 64),
		queries:  make(chan chan domain.Event, 4),
		speed:    1,
	}
	s.Start()

	evs := s.Apply(PlaceOrder{Pickup: "a", Destination: "b"})
	if len(evs) != 2 || evs[0].Type != domain.EventOrderPlaced || evs[1].Type != domain.EventOrderUnassignable {
		t.Fatalf("expected placed+unassignable, got %v", types(evs))
	}
	if len(s.pendingOrders) != 0 {
		t.Fatalf("a genuinely unreachable order must not be queued, got %v", s.pendingOrders)
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

// The estimate the assignment card shows a visitor, and the one every
// pickup-time metric on the comparison page is derived from, has to be the
// virtual time the driver genuinely arrives at. It used to add the routed
// distance to the clock instead of the number of hops, which is a different
// unit entirely - roughly one edge length per hop too large - so the card
// promised a pickup at ~294 for a driver that arrived at 3.
func TestPickupEstimateMatchesWhenTheDriverActuallyArrives(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()

	var estimate float64
	var driverID domain.DriverID
	for _, e := range s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest}) {
		if e.Type != domain.EventOrderAssigned {
			continue
		}
		p := e.Payload.(map[string]any)
		estimate = p["pickupEtaVirtualTime"].(float64)
		driverID = p["driverId"].(domain.DriverID)
	}
	if driverID == "" {
		t.Fatal("expected the order to be assigned")
	}

	var arrivedAt float64
	for i := 0; i < 300 && arrivedAt == 0; i++ {
		for _, e := range s.Advance() {
			if e.Type != domain.EventDriverStatusChanged {
				continue
			}
			p := e.Payload.(map[string]any)
			if p["driverId"] == driverID && p["status"] == domain.DriverDelivering {
				arrivedAt = e.VirtualTime
			}
		}
	}

	if arrivedAt == 0 {
		t.Fatal("expected the driver to reach the pickup")
	}
	// the ETA is a continuous distance/speed estimate, but arrival only
	// happens at a tick boundary, so the real arrival can be up to one tick
	// later than the estimate.
	if estimate > arrivedAt || arrivedAt-estimate > virtualStepPerTick {
		t.Fatalf("estimated pickup at virtual time %.1f but the driver arrived at %.1f", estimate, arrivedAt)
	}
}

// A driver standing on the pickup when it is assigned has already collected
// the order. tick only checks for pickup arrival *after* moving a driver, so
// this case used to run the whole delivery still marked en-route-to-pickup,
// with the order never entering en_route - the delivery still completed,
// which is why the lifecycle test above (whose pickup is not a driver's
// starting node) never caught it.
func TestDriverAlreadyAtPickupIsDeliveringImmediately(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()

	// ordering a pickup at a driver's own position is a zero-distance
	// assignment, so that driver necessarily wins it.
	pickup := s.drivers["driver-0"].Position
	events := s.Apply(PlaceOrder{Pickup: pickup, Destination: scenarioDest})

	d := s.drivers["driver-0"]
	if d.AssignedOrder == "" {
		t.Fatalf("expected driver-0 to take the order placed at its own position, got %+v", d)
	}
	if d.Status != domain.DriverDelivering {
		t.Fatalf("a driver already at the pickup should be delivering, got %s", d.Status)
	}
	if order := s.orders[d.AssignedOrder]; order.Status != domain.OrderEnRoute {
		t.Fatalf("an order collected at assignment should be en route, got %s", order.Status)
	}

	// the emitted event has to agree, since that is what a browser renders.
	var emitted any
	for _, e := range events {
		if e.Type == domain.EventDriverStatusChanged {
			emitted = e.Payload.(map[string]any)["status"]
		}
	}
	if emitted != domain.DriverDelivering {
		t.Fatalf("emitted driver status was %v, want %s", emitted, domain.DriverDelivering)
	}

	// and it must still finish rather than stalling on the pickup node.
	delivered := false
	for i := 0; i < 300 && !delivered; i++ {
		for _, e := range s.Advance() {
			if e.Type == domain.EventOrderDelivered {
				delivered = true
			}
		}
		checkInvariants(t, s)
	}
	if !delivered {
		t.Fatal("expected the delivery to complete")
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
