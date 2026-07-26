package simulation

import (
	"fmt"
	"testing"

	"dispatchlab/internal/domain"
)

// benchNodes are corners of the default generated city, far enough apart that
// an order takes real routing and several ticks of movement to complete.
const (
	benchPickup      = domain.NodeID("n-0-0")
	benchDestination = domain.NodeID("n-5-5")
)

// BenchmarkAdvance measures the per-tick cost of a simulation carrying a
// steady load of in-flight orders. This is the number that decides how many
// concurrent runs one process can host: every run pays it on every tick.
func BenchmarkAdvance(b *testing.B) {
	for _, drivers := range []int{8, 40, 200} {
		b.Run(fmt.Sprintf("drivers=%d", drivers), func(b *testing.B) {
			sim := New("bench", 1, drivers)
			sim.Start()
			for i := 0; i < drivers/2; i++ {
				sim.Apply(PlaceOrder{Pickup: benchPickup, Destination: benchDestination})
			}

			b.ReportAllocs()
			b.ResetTimer()
			events := 0
			for i := 0; i < b.N; i++ {
				events += len(sim.Advance())
			}
			b.StopTimer()

			// events/op is the throughput figure; ns/op alone would hide a
			// tick that got cheaper only because it stopped emitting.
			b.ReportMetric(float64(events)/float64(b.N), "events/op")
		})
	}
}

// BenchmarkPlaceOrder covers the command path a visitor actually triggers:
// matching plus routing plus event emission, under each strategy. Optimized
// may assign inside PlaceOrder when the order is alone with an idle driver;
// contended orders still queue for the tick path. Use BenchmarkFullScenario
// for an honest end-to-end cost comparison.
func BenchmarkPlaceOrder(b *testing.B) {
	strategies := []struct {
		name     string
		strategy MatchingStrategy
	}{
		{"baseline", StrategyBaseline},
		{"optimized", StrategyOptimized},
	}

	for _, s := range strategies {
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				sim := NewWithConfig(Config{ID: "bench", Seed: 1, DriverCount: 40, Strategy: s.strategy})
				sim.Start()
				b.StartTimer()

				sim.Apply(PlaceOrder{Pickup: benchPickup, Destination: benchDestination})
			}
		})
	}
}

// BenchmarkFullScenario runs an entire deterministic scenario end to end
// under each strategy. Unlike BenchmarkPlaceOrder this does include the batch
// solve, so it is the honest per-strategy cost comparison.
func BenchmarkFullScenario(b *testing.B) {
	strategies := []struct {
		name     string
		strategy MatchingStrategy
	}{
		{"baseline", StrategyBaseline},
		{"optimized", StrategyOptimized},
	}

	for _, s := range strategies {
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sim := NewWithConfig(Config{ID: "bench", Seed: 1, DriverCount: 20, Strategy: s.strategy})
				sim.Start()
				for order := 0; order < 20; order++ {
					sim.Apply(PlaceOrder{Pickup: benchPickup, Destination: benchDestination})
					sim.Advance()
				}
				for tick := 0; tick < 100; tick++ {
					sim.Advance()
				}
			}
		})
	}
}

// BenchmarkCloseRoad measures a closure over a city with many active routes,
// since its cost is dominated by rerouting every driver whose remaining path
// crossed the closed edge rather than by the closure itself.
func BenchmarkCloseRoad(b *testing.B) {
	edgeID := func(sim *Simulation) domain.EdgeID {
		for _, edges := range sim.City.Edges {
			if len(edges) > 0 {
				return edges[0].ID
			}
		}
		b.Fatal("the generated city has no edges")
		return ""
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		sim := New("bench", 1, 40)
		sim.Start()
		for order := 0; order < 20; order++ {
			sim.Apply(PlaceOrder{Pickup: benchPickup, Destination: benchDestination})
		}
		sim.Advance()
		target := edgeID(sim)
		b.StartTimer()

		sim.Apply(CloseRoad{EdgeID: target})
	}
}
