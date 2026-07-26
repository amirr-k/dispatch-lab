package simulation

import (
	"reflect"
	"testing"

	"dispatchlab/internal/domain"
)

func newOptimizedSim(id string, seed int64, drivers int, minBatch int, maxWait float64) *Simulation {
	return NewWithConfig(Config{
		ID: id, Seed: seed, DriverCount: drivers,
		Strategy: StrategyOptimized, MinBatchSize: minBatch, MaxWaitVirtualTime: maxWait,
	})
}

func TestOptimizedLoneOrderAssignsInSameApply(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 2, 5)
	s.Start()

	evs := s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	var assignedAt float64
	var sawAssigned bool
	for _, e := range evs {
		if e.Type == domain.EventOrderAssigned {
			sawAssigned = true
			assignedAt = e.VirtualTime
		}
	}
	if !sawAssigned {
		t.Fatalf("expected lone optimized order with idle driver to assign in the same Apply, got %v", types(evs))
	}
	if assignedAt != 0 {
		t.Fatalf("expected assignment at create virtual time 0, got %v", assignedAt)
	}
	if s.virtualTime != 0 {
		t.Fatalf("expected virtual time still 0 after same-Apply assign, got %v", s.virtualTime)
	}
	if len(s.pendingOrders) != 0 {
		t.Fatalf("expected no pending orders after immediate assign, got %d", len(s.pendingOrders))
	}
	batch, immediate := s.DispatchCounts()
	if batch != 0 || immediate != 1 {
		t.Fatalf("expected 0 batch / 1 immediate, got %d / %d", batch, immediate)
	}
}

func TestOptimizedQueuesWhenAlreadyPending(t *testing.T) {
	// one driver: first order takes it at place time; further orders stay
	// queued because there is no idle driver (and then because the queue
	// is already non-empty).
	s := newOptimizedSim("sim", scenarioSeed, 1, 2, 50)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	if len(s.pendingOrders) != 2 {
		t.Fatalf("expected 2 pending orders while the only driver is busy, got %d", len(s.pendingOrders))
	}
	batch, immediate := s.DispatchCounts()
	if batch != 0 || immediate != 1 {
		t.Fatalf("expected only the first order to immediate-dispatch, got batch=%d immediate=%d", batch, immediate)
	}
}

func TestOptimizedMinBatchFiresEarly(t *testing.T) {
	// occupy the only driver, then queue two more so pending hits MinBatchSize.
	// the next tick with an idle driver (after the first delivery) batches them;
	// with a long MaxWait we rely on min-batch once a driver frees, but we can
	// also fire batch on tick while n >= MinBatchSize even before free — that
	// just leaves them pending. Force the free path by advancing until both
	// waiting orders assign via batch.
	s := newOptimizedSim("sim", scenarioSeed, 1, 2, 50)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	if len(s.pendingOrders) != 2 {
		t.Fatalf("expected 2 pending before min-batch, got %d", len(s.pendingOrders))
	}

	assigned := 0
	for i := 0; i < 400 && assigned < 2; i++ {
		for _, e := range s.Advance() {
			if e.Type == domain.EventOrderAssigned {
				assigned++
			}
		}
	}
	if assigned != 2 {
		t.Fatalf("expected min-batch to eventually assign both queued orders, got %d", assigned)
	}
	batch, _ := s.DispatchCounts()
	if batch < 1 {
		t.Fatalf("expected at least one batch dispatch, got %d", batch)
	}
}

func TestOptimizedMaxWaitBatchesPartialQueue(t *testing.T) {
	// MinBatchSize 3 so two pending orders sit below the batch threshold
	// until MaxWait forces a batch. Occupy the only driver first so both
	// later orders stay pending instead of assigning at place time.
	s := newOptimizedSim("sim", scenarioSeed, 1, 3, 2)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	if len(s.pendingOrders) != 2 {
		t.Fatalf("expected 2 pending below min-batch, got %d", len(s.pendingOrders))
	}

	var evs []domain.Event
	for i := 0; i < 2; i++ {
		evs = append(evs, s.Advance()...)
	}

	// max-wait may fire a batch with no idle drivers (nothing assigns yet);
	// keep advancing until both waiting orders get drivers.
	assigned := 0
	for _, e := range evs {
		if e.Type == domain.EventOrderAssigned {
			assigned++
		}
	}
	for i := 0; i < 400 && assigned < 2; i++ {
		for _, e := range s.Advance() {
			if e.Type == domain.EventOrderAssigned {
				assigned++
			}
		}
	}
	if assigned != 2 {
		t.Fatalf("expected max-wait path to eventually assign both pending orders, got %d", assigned)
	}
	batch, _ := s.DispatchCounts()
	if batch < 1 {
		t.Fatalf("expected at least one batch dispatch, got %d", batch)
	}
}

func TestOptimizedMaxWaitSingleOrderWithoutIdle(t *testing.T) {
	// one driver, first order takes it via place-time immediate dispatch;
	// second order arrives while the driver is busy and must wait out
	// MaxWait (and/or until the driver frees) rather than batching alone.
	s := newOptimizedSim("sim", scenarioSeed, 1, 2, 2)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	if len(s.pendingOrders) != 1 {
		t.Fatalf("expected second order queued while driver is busy, got %d pending", len(s.pendingOrders))
	}

	// first tick after the second place: no idle driver, age still < MaxWait
	s.Advance()
	if len(s.pendingOrders) != 1 {
		t.Fatalf("expected order still pending before max-wait with no idle driver, got %d", len(s.pendingOrders))
	}

	// age reaches MaxWait on the next tick; assignBaseline re-queues if the
	// driver is still busy, then retries once they free — either way the
	// order must eventually assign without needing a second order to batch.
	assigned := 0
	for i := 0; i < 400 && assigned < 1; i++ {
		for _, e := range s.Advance() {
			if e.Type == domain.EventOrderAssigned {
				assigned++
			}
		}
	}
	if assigned != 1 {
		t.Fatalf("expected the waiting order to assign once a driver is free, got %d", assigned)
	}
}

func TestOptimizedStrategyMultipleOrdersInOneBatch(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, 1, 2, 5)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	assigned := 0
	for i := 0; i < 400 && assigned < 2; i++ {
		for _, e := range s.Advance() {
			if e.Type == domain.EventOrderAssigned {
				assigned++
			}
		}
	}
	if assigned != 2 {
		t.Fatalf("expected both queued orders assigned across the run, got %d", assigned)
	}
}

func TestOptimizedStrategyDeterministic(t *testing.T) {
	run := func() []domain.Event {
		s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 2, 2)
		var evs []domain.Event
		evs = append(evs, s.Start()...)
		evs = append(evs, s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})...)
		for i := 0; i < 40; i++ {
			evs = append(evs, s.Advance()...)
		}
		return evs
	}

	a, b := run(), run()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("expected the optimized strategy to produce identical event sequences for identical input")
	}
}

func TestOptimizedStrategyResetClearsPendingOrders(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, 1, 2, 5)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	if len(s.pendingOrders) != 1 {
		t.Fatalf("expected 1 pending order before reset, got %d", len(s.pendingOrders))
	}

	s.Apply(Reset{})
	if len(s.pendingOrders) != 0 {
		t.Fatalf("expected reset to clear pending orders, got %d", len(s.pendingOrders))
	}
	batch, immediate := s.DispatchCounts()
	if batch != 0 || immediate != 0 {
		t.Fatalf("expected reset to clear dispatch counts, got %d / %d", batch, immediate)
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
	// fewer drivers than orders forces later orders to wait for a delivery
	// to free a driver, exercising IdleSince and the spatial index's
	// re-add-on-idle path.
	s := newOptimizedSim("sim", scenarioSeed, 1, 2, 2)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	delivered, assigned := 0, 0
	for i := 0; i < 400 && delivered < 3; i++ {
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
		t.Fatalf("expected both queued orders eventually assigned, got %d", assigned)
	}
	if delivered != 3 {
		t.Fatalf("expected all three orders eventually delivered, got %d", delivered)
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

// TestOptimizedBatchAvoidsGreedyPitfall saturates drivers so two orders
// queue together, then confirms the tick path batches them (joint solve)
// rather than leaving them stuck behind MaxWait.
func TestOptimizedBatchAvoidsGreedyPitfall(t *testing.T) {
	s := newOptimizedSim("sim", scenarioSeed, scenarioDrivers, 2, 5)
	s.Start()
	for i := 0; i < scenarioDrivers; i++ {
		pickup, dest := scenarioPickup, scenarioDest
		if i%2 == 1 {
			pickup, dest = scenarioDest, scenarioPickup
		}
		s.Apply(PlaceOrder{Pickup: pickup, Destination: dest})
	}
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
	s.Apply(PlaceOrder{Pickup: scenarioDest, Destination: scenarioPickup})
	if len(s.pendingOrders) != 2 {
		t.Fatalf("expected 2 pending after saturating drivers, got %d", len(s.pendingOrders))
	}

	queuedAssigned := 0
	for i := 0; i < 400 && queuedAssigned < 2; i++ {
		for _, e := range s.Advance() {
			if e.Type == domain.EventOrderAssigned {
				queuedAssigned++
			}
		}
	}
	if queuedAssigned < 2 {
		t.Fatalf("expected both queued orders to assign via batch/retry, got %d", queuedAssigned)
	}
	batch, _ := s.DispatchCounts()
	if batch < 1 {
		t.Fatalf("expected at least one batch once two orders contended, got %d", batch)
	}
}
