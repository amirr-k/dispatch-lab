package matching

import (
	"reflect"
	"testing"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/spatial"
)

// crossedCostCity is hand-built so driver A is nearest order o1's pickup and
// driver B is nearest o2's pickup, but assigning both drivers "greedily"
// (nearest order first) forces a catastrophic pairing - the textbook case
// where sequential nearest-first matching is far from optimal and joint
// batch optimization is not. Matching only ever routes driver -> pickup, so
// P1/P2 deliberately have no outgoing edges: that rules out any shortcut
// through them and makes the four direct distances exact.
func crossedCostCity() *domain.City {
	edge := func(from, to domain.NodeID, weight float64) domain.Edge {
		return domain.Edge{ID: domain.EdgeID("e-" + string(from) + "-" + string(to)), From: from, To: to, Weight: weight}
	}
	nodes := map[domain.NodeID]domain.Node{
		"A":  {ID: "A"},
		"B":  {ID: "B"},
		"P1": {ID: "P1"},
		"P2": {ID: "P2"},
	}
	return &domain.City{
		Nodes: nodes,
		Edges: map[domain.NodeID][]domain.Edge{
			"A": {edge("A", "P1", 1), edge("A", "P2", 2)},
			"B": {edge("B", "P1", 2), edge("B", "P2", 100)},
		},
	}
}

func order(id domain.OrderID, pickup domain.NodeID, createdAt float64) *domain.Order {
	return &domain.Order{ID: id, Pickup: pickup, Destination: pickup, CreatedAtVirtualTime: createdAt, Status: domain.OrderPending}
}

func gridAt(coords map[domain.DriverID]spatial.Point) *spatial.Grid {
	g := spatial.NewGrid(10)
	for id, p := range coords {
		g.Set(string(id), p)
	}
	return g
}

func totalPickupDistance(assignments []Assignment) float64 {
	var sum float64
	for _, a := range assignments {
		sum += a.ToPickup.Distance
	}
	return sum
}

func TestOptimizedBeatsGreedyOnCrossedCosts(t *testing.T) {
	c := crossedCostCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"A": driver("A", "A", domain.DriverIdle),
		"B": driver("B", "B", domain.DriverIdle),
	}
	orders := []*domain.Order{order("o1", "P1", 0), order("o2", "P2", 0)}

	greedy, _ := BaselineBatch(c, drivers, orders)
	if got := totalPickupDistance(greedy); got != 101 {
		t.Fatalf("expected greedy nearest-first to land on the 1+100=101 total, got %v (%+v)", got, greedy)
	}

	index := gridAt(map[domain.DriverID]spatial.Point{"A": {}, "B": {}})
	assigned, infeasible, _ := Optimized(c, drivers, orders, index, 10, DefaultCostWeights(), 0)
	if len(infeasible) != 0 {
		t.Fatalf("expected no infeasible orders, got %v", infeasible)
	}
	if len(assigned) != 2 {
		t.Fatalf("expected both orders assigned, got %+v", assigned)
	}
	total := totalPickupDistance(assigned)
	if total != 4 {
		t.Fatalf("expected optimized batching to find the 2+2=4 total, got %v (%+v)", total, assigned)
	}
	if total >= 101 {
		t.Fatalf("expected optimized total (%v) to beat greedy's 101", total)
	}
}

func TestOptimizedDeterministic(t *testing.T) {
	c := crossedCostCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"A": driver("A", "A", domain.DriverIdle),
		"B": driver("B", "B", domain.DriverIdle),
	}
	orders := []*domain.Order{order("o1", "P1", 0), order("o2", "P2", 0)}
	index := gridAt(map[domain.DriverID]spatial.Point{"A": {}, "B": {}})

	a, ai, _ := Optimized(c, drivers, orders, index, 10, DefaultCostWeights(), 0)
	b, bi, _ := Optimized(c, drivers, orders, index, 10, DefaultCostWeights(), 0)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ai, bi) {
		t.Fatalf("expected deterministic results, got %+v/%+v vs %+v/%+v", a, ai, b, bi)
	}
}

func TestOptimizedMarksGenuinelyInfeasibleOrders(t *testing.T) {
	c := crossedCostCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"A": driver("A", "A", domain.DriverIdle),
	}
	// isolated pickup with no edge to it at all: no driver can ever reach it
	c.Nodes["isolated"] = domain.Node{ID: "isolated"}
	orders := []*domain.Order{order("o1", "P1", 0), order("o2", "isolated", 0)}
	index := gridAt(map[domain.DriverID]spatial.Point{"A": {}})

	assigned, infeasible, _ := Optimized(c, drivers, orders, index, 10, DefaultCostWeights(), 0)
	if len(assigned) != 1 || assigned[0].OrderID != "o1" {
		t.Fatalf("expected o1 assigned, got %+v", assigned)
	}
	if len(infeasible) != 1 || infeasible[0] != "o2" {
		t.Fatalf("expected o2 reported infeasible, got %v", infeasible)
	}
}

func TestOptimizedLeavesUncompetitiveOrdersUnassignedButNotInfeasible(t *testing.T) {
	// one driver, three reachable orders: only one can be matched this
	// round. The other two lost the competition, not "infeasible" - they
	// can be retried in the next batch window.
	c := crossedCostCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"A": driver("A", "A", domain.DriverIdle),
	}
	orders := []*domain.Order{order("o1", "P1", 0), order("o2", "P2", 0)}
	index := gridAt(map[domain.DriverID]spatial.Point{"A": {}})

	assigned, infeasible, _ := Optimized(c, drivers, orders, index, 10, DefaultCostWeights(), 0)
	if len(assigned) != 1 {
		t.Fatalf("expected exactly 1 order assigned (only 1 driver), got %+v", assigned)
	}
	if len(infeasible) != 0 {
		t.Fatalf("expected neither reachable order marked infeasible, got %v", infeasible)
	}
}

func TestOptimizedNoOrders(t *testing.T) {
	assigned, infeasible, ms := Optimized(crossedCostCity(), nil, nil, spatial.NewGrid(10), 5, DefaultCostWeights(), 0)
	if assigned != nil || infeasible != nil || ms != 0 {
		t.Fatalf("expected all-zero result for no orders, got %+v %v %v", assigned, infeasible, ms)
	}
}

func TestOptimizedWaitTimeFavorsOlderOrders(t *testing.T) {
	// two orders competing for one driver at equal pickup distance; the
	// order that's been waiting longer should win once WaitTime is weighted
	edge := func(from, to domain.NodeID) domain.Edge {
		return domain.Edge{ID: domain.EdgeID("e-" + string(from) + "-" + string(to)), From: from, To: to, Weight: 1}
	}
	c := &domain.City{
		Nodes: map[domain.NodeID]domain.Node{"A": {ID: "A"}, "P1": {ID: "P1"}, "P2": {ID: "P2"}},
		Edges: map[domain.NodeID][]domain.Edge{
			"A":  {edge("A", "P1"), edge("A", "P2")},
			"P1": {edge("P1", "A")},
			"P2": {edge("P2", "A")},
		},
	}
	drivers := map[domain.DriverID]*domain.Driver{"A": driver("A", "A", domain.DriverIdle)}
	orders := []*domain.Order{order("old", "P1", 0), order("new", "P2", 5)}
	index := gridAt(map[domain.DriverID]spatial.Point{"A": {}})

	assigned, _, _ := Optimized(c, drivers, orders, index, 10, DefaultCostWeights(), 5)
	if len(assigned) != 1 || assigned[0].OrderID != "old" {
		t.Fatalf("expected the older order to win an equal-distance tie once wait time is weighted, got %+v", assigned)
	}
}
