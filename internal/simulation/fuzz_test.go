package simulation

import (
	"testing"

	"dispatchlab/internal/domain"
)

// FuzzPlaceOrder throws arbitrary pickup/destination node identifiers at the
// simulation. Garbage nodes must never panic or corrupt state: they should
// resolve to an unassignable order, and invariants must always hold.
func FuzzPlaceOrder(f *testing.F) {
	f.Add("n-0-0", "n-5-5")
	f.Add("", "")
	f.Add("n-0-5", "does-not-exist")
	f.Add("💥", "n-1-1")

	f.Fuzz(func(t *testing.T, pickup, dest string) {
		s := New("fuzz", 1, 4)
		s.Start()
		evs := s.Apply(PlaceOrder{Pickup: domain.NodeID(pickup), Destination: domain.NodeID(dest)})

		if len(evs) == 0 || evs[0].Type != domain.EventOrderPlaced {
			t.Fatalf("every command must at least emit order.placed, got %v", types(evs))
		}
		for i := 0; i < 20; i++ {
			s.Advance()
			checkInvariants(t, s)
		}
	})
}

// FuzzPlaceOrderOptimized is FuzzPlaceOrder's counterpart for the batching
// path: garbage node identifiers must never panic or corrupt state under
// StrategyOptimized either, exercising the pending-order queue and the
// spatial index alongside the usual invariants.
func FuzzPlaceOrderOptimized(f *testing.F) {
	f.Add("n-0-0", "n-5-5")
	f.Add("", "")
	f.Add("n-0-5", "does-not-exist")
	f.Add("💥", "n-1-1")

	f.Fuzz(func(t *testing.T, pickup, dest string) {
		s := newOptimizedSim("fuzz-optimized", 1, 4, 3)
		s.Start()
		evs := s.Apply(PlaceOrder{Pickup: domain.NodeID(pickup), Destination: domain.NodeID(dest)})

		if len(evs) == 0 || evs[0].Type != domain.EventOrderPlaced {
			t.Fatalf("every command must at least emit order.placed, got %v", types(evs))
		}
		for i := 0; i < 20; i++ {
			s.Advance()
			checkInvariants(t, s)
		}
	})
}
