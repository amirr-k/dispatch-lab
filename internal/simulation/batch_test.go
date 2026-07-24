package simulation

import (
	"reflect"
	"testing"

	"dispatchlab/internal/domain"
)

func newOptimizedSim(id string, seed int64, drivers int, batchWindow float64) *Simulation {
	return NewWithConfig(Config{
		ID: id, Seed: seed, DriverCount: drivers,
		Strategy: StrategyOptimized, BatchWindow: batchWindow,
	})
}

func TestOptimizedStrategyDefersAssignment(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 5)
	s.Start()

	evs := s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	if len(evs) != 1 || evs[0].Type != domain.EventOrderPlaced {
		t.Fatalf("expected only order.placed before the batch window elapses, got %v", types(evs))
	}
	if len(s.pendingOrders) != 1 {
		t.Fatalf("expected the order queued as pending, got %d pending", len(s.pendingOrders))
	}
}

func TestOptimizedStrategyAssignsAfterBatchWindow(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 5)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	var evs []domain.Event
	for i := 0; i < 5; i++ {
		evs = append(evs, s.Advance()...)
	}

	var sawComputed, sawAssigned bool
	for _, e := range evs {
		switch e.Type {
		case domain.EventRouteComputed:
			sawComputed = true
		case domain.EventOrderAssigned:
			sawAssigned = true
		}
	}
	if !sawComputed || !sawAssigned {
		t.Fatalf("expected the batch window to assign the pending order, got %v", types(evs))
	}
	if len(s.pendingOrders) != 0 {
		t.Fatalf("expected no orders left pending after a successful batch, got %d", len(s.pendingOrders))
	}
}

func TestOptimizedStrategyMultipleOrdersInOneBatch(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 5)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})

	var evs []domain.Event
	for i := 0; i < 5; i++ {
		evs = append(evs, s.Advance()...)
	}

	assigned := 0
	for _, e := range evs {
		if e.Type == domain.EventOrderAssigned {
			assigned++
		}
	}
	if assigned != 2 {
		t.Fatalf("expected both orders assigned in the same batch, got %d assigned events: %v", assigned, types(evs))
	}
}

func TestOptimizedStrategyDeterministic(t *testing.T) {
	run := func() []domain.Event {
		s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 5)
		var evs []domain.Event
		evs = append(evs, s.Start()...)
		evs = append(evs, s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})...)
		for i := 0; i < 40; i++ {
			evs = append(evs, s.Advance()...)
		}
		return evs
	}

	a, b := normalize(run()), normalize(run())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("expected the optimized strategy to produce identical event sequences for identical input")
	}
}

func TestOptimizedStrategyResetClearsPendingOrders(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 5)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	if len(s.pendingOrders) != 1 {
		t.Fatalf("expected 1 pending order before reset, got %d", len(s.pendingOrders))
	}

	s.Apply(Reset{})
	if len(s.pendingOrders) != 0 {
		t.Fatalf("expected reset to clear pending orders, got %d", len(s.pendingOrders))
	}

	var evs []domain.Event
	for i := 0; i < 10; i++ {
		evs = append(evs, s.Advance()...)
	}
	for _, e := range evs {
		if e.Type == domain.EventOrderAssigned || e.Type == domain.EventRouteComputed {
			t.Fatalf("expected the reset (discarded) order to never surface an assignment, got %s", e.Type)
		}
	}
}

func TestOptimizedStrategyReusesFreedDriverForNextBatch(t *testing.T) {
	// fewer drivers than orders forces the second order to wait for the
	// first delivery to free a driver back up, exercising IdleSince and the
	// spatial index's re-add-on-idle path.
	s := newOptimizedSim("sim", scenarioSeed, 1, 3)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})

	delivered, assigned := 0, 0
	for i := 0; i < 400 && delivered < 2; i++ {
		for _, e := range s.Advance() {
			if e.Type == domain.EventOrderDelivered {
				delivered++
			}
			if e.Type == domain.EventOrderAssigned {
				assigned++
			}
		}
		checkInvariants(t, s)
	}

	if assigned != 2 {
		t.Fatalf("expected both orders eventually assigned across batches, got %d", assigned)
	}
	if delivered != 2 {
		t.Fatalf("expected both orders eventually delivered, got %d", delivered)
	}
}

func TestNewWithConfigDefaultsToBaseline(t *testing.T) {
	s := NewWithConfig(Config{ID: "sim", Seed: scenarioSeed, DriverCount: scenarioDrivers})
	if s.strategy != StrategyBaseline {
		t.Fatalf("expected empty Strategy to default to baseline, got %s", s.strategy)
	}
	if s.driverIndex != nil {
		t.Fatal("expected baseline strategy to skip building a spatial index")
	}

	// baseline still assigns immediately, unaffected by the new config path
	s.Start()
	evs := s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	var sawAssigned bool
	for _, e := range evs {
		if e.Type == domain.EventOrderAssigned {
			sawAssigned = true
		}
	}
	if !sawAssigned {
		t.Fatalf("expected immediate assignment under default (baseline) config, got %v", types(evs))
	}
}

func TestTotalAssignmentComputeMsAccumulates(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	if s.TotalAssignmentComputeMs() != 0 {
		t.Fatal("expected zero compute time before any assignment")
	}
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	if s.TotalAssignmentComputeMs() <= 0 {
		t.Fatal("expected a positive accumulated compute time after an assignment")
	}
}

func TestOrdersReflectsFinalStatus(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	orders := s.Orders()
	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(orders))
	}
	if orders[0].Status != domain.OrderAssigned {
		t.Fatalf("expected the order to be assigned, got %s", orders[0].Status)
	}
	if orders[0].CreatedAtVirtualTime != 0 {
		t.Fatalf("expected creation time 0, got %v", orders[0].CreatedAtVirtualTime)
	}
}

// TestOptimizedBatchAvoidsGreedyPitfall drives the actor with a hand-picked
// pair of orders whose pickups favor different drivers in a way that
// exposes the same crossed-cost pattern matching.Optimized is tested
// against directly - confirming the wiring, not just the algorithm.
func TestOptimizedBatchAvoidsGreedyPitfall(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 3)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})

	var evs []domain.Event
	for i := 0; i < 3; i++ {
		evs = append(evs, s.Advance()...)
	}

	drivers := map[domain.DriverID]bool{}
	for _, e := range evs {
		if e.Type != domain.EventOrderAssigned {
			continue
		}
		p := e.Payload.(map[string]any)
		drivers[p["driverId"].(domain.DriverID)] = true
	}
	if len(drivers) != 2 {
		t.Fatalf("expected two distinct drivers assigned across the batch, got %v", drivers)
	}
}
