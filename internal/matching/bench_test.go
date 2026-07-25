package matching

import (
	"fmt"
	"math/rand"
	"testing"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
	"dispatchlab/internal/spatial"
)

const (
	benchSide    = 20
	benchSpacing = 100.0
)

// benchFixture builds a city with drivers scattered over it and a batch of
// orders, all from a fixed seed so a benchmark run is comparable to the one
// before it.
type benchFixture struct {
	city    *domain.City
	drivers map[domain.DriverID]*domain.Driver
	orders  []*domain.Order
	index   *spatial.Grid
}

func newBenchFixture(driverCount, orderCount int) benchFixture {
	c := city.GenerateGrid(city.GridConfig{
		Seed:           1,
		Rows:           benchSide,
		Cols:           benchSide,
		CellSpacing:    benchSpacing,
		JitterFraction: 0.15,
	})

	nodeIDs := make([]domain.NodeID, 0, benchSide*benchSide)
	for r := 0; r < benchSide; r++ {
		for col := 0; col < benchSide; col++ {
			nodeIDs = append(nodeIDs, domain.NodeID(fmt.Sprintf("n-%d-%d", r, col)))
		}
	}

	rng := rand.New(rand.NewSource(42))
	index := spatial.NewGrid(benchSpacing)
	drivers := make(map[domain.DriverID]*domain.Driver, driverCount)
	for i := 0; i < driverCount; i++ {
		id := domain.DriverID(fmt.Sprintf("d-%d", i))
		at := nodeIDs[rng.Intn(len(nodeIDs))]
		drivers[id] = &domain.Driver{ID: id, Position: at, Status: domain.DriverIdle}
		node := c.Nodes[at]
		index.Set(string(id), spatial.Point{X: node.X, Y: node.Y})
	}

	orders := make([]*domain.Order, 0, orderCount)
	for i := 0; i < orderCount; i++ {
		orders = append(orders, &domain.Order{
			ID:          domain.OrderID(fmt.Sprintf("o-%d", i)),
			Pickup:      nodeIDs[rng.Intn(len(nodeIDs))],
			Destination: nodeIDs[rng.Intn(len(nodeIDs))],
			Status:      domain.OrderPending,
		})
	}

	return benchFixture{city: c, drivers: drivers, orders: orders, index: index}
}

// batchShapes covers the realistic demo load (a handful of drivers, a few
// orders) through batches far larger than the public demo permits, so the
// cost curve rather than one operating point is on record.
var batchShapes = []struct{ drivers, orders int }{
	{12, 5},
	{40, 20},
	{200, 50},
	{500, 100},
}

// BenchmarkBaselineBatch and BenchmarkOptimized are deliberately the same
// shapes: the pair is what backs any claim about what optimized matching
// costs relative to greedy.
func BenchmarkBaselineBatch(b *testing.B) {
	for _, shape := range batchShapes {
		f := newBenchFixture(shape.drivers, shape.orders)
		b.Run(fmt.Sprintf("drivers=%d_orders=%d", shape.drivers, shape.orders), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				BaselineBatch(f.city, f.drivers, f.orders)
			}
		})
	}
}

func BenchmarkOptimized(b *testing.B) {
	for _, shape := range batchShapes {
		f := newBenchFixture(shape.drivers, shape.orders)
		b.Run(fmt.Sprintf("drivers=%d_orders=%d", shape.drivers, shape.orders), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				Optimized(f.city, f.drivers, f.orders, f.index, 10, DefaultCostWeights(), 0)
			}
		})
	}
}

// BenchmarkOptimizedByCandidateSet isolates the one knob that trades matching
// quality for cost: how many drivers per order enter the cost matrix. The
// spatial index exists to keep this bounded, and this is the measurement that
// says what each extra candidate buys.
func BenchmarkOptimizedByCandidateSet(b *testing.B) {
	f := newBenchFixture(200, 50)

	for _, candidates := range []int{3, 5, 10, 25, 50} {
		b.Run(fmt.Sprintf("candidates=%d", candidates), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				Optimized(f.city, f.drivers, f.orders, f.index, candidates, DefaultCostWeights(), 0)
			}
		})
	}
}

// BenchmarkMinCostAssignment measures the Hungarian solver alone, with no
// routing in the loop, so a regression in the O(n^3) core is not hidden by
// the A* calls that dominate Optimized.
func BenchmarkMinCostAssignment(b *testing.B) {
	for _, n := range []int{5, 10, 25, 50, 100} {
		rng := rand.New(rand.NewSource(7))
		cost := make([][]float64, n)
		for i := range cost {
			cost[i] = make([]float64, n)
			for j := range cost[i] {
				cost[i][j] = rng.Float64() * 1000
			}
		}

		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				MinCostAssignment(cost)
			}
		})
	}
}

// BenchmarkSpatialCandidates covers the index on its own: the whole reason
// matching does not compare every order against every driver.
func BenchmarkSpatialCandidates(b *testing.B) {
	for _, driverCount := range []int{100, 1000, 10000} {
		rng := rand.New(rand.NewSource(3))
		index := spatial.NewGrid(benchSpacing)
		for i := 0; i < driverCount; i++ {
			index.Set(fmt.Sprintf("d-%d", i), spatial.Point{
				X: rng.Float64() * benchSide * benchSpacing,
				Y: rng.Float64() * benchSide * benchSpacing,
			})
		}
		query := spatial.Point{X: benchSide * benchSpacing / 2, Y: benchSide * benchSpacing / 2}

		b.Run(fmt.Sprintf("drivers=%d", driverCount), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				index.Candidates(query, 10)
			}
		})
	}
}
