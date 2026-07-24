package matching

import (
	"testing"

	"dispatchlab/internal/domain"
)

// lineCity is a-b-c in a row (unit edges) plus an isolated node d.
func lineCity() *domain.City {
	edge := func(from, to domain.NodeID) domain.Edge {
		return domain.Edge{ID: domain.EdgeID("e-" + string(from) + "-" + string(to)), From: from, To: to, Weight: 1}
	}
	return &domain.City{
		Nodes: map[domain.NodeID]domain.Node{
			"a": {ID: "a", X: 0, Y: 0},
			"b": {ID: "b", X: 1, Y: 0},
			"c": {ID: "c", X: 2, Y: 0},
			"d": {ID: "d", X: 9, Y: 9},
		},
		Edges: map[domain.NodeID][]domain.Edge{
			"a": {edge("a", "b")},
			"b": {edge("b", "a"), edge("b", "c")},
			"c": {edge("c", "b")},
		},
	}
}

func driver(id domain.DriverID, pos domain.NodeID, status domain.DriverStatus) *domain.Driver {
	return &domain.Driver{ID: id, Position: pos, Status: status}
}

func TestBaselinePicksNearest(t *testing.T) {
	c := lineCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"d1": driver("d1", "a", domain.DriverIdle), // distance 2 to c
		"d2": driver("d2", "b", domain.DriverIdle), // distance 1 to c
	}
	got, route, ok := Baseline(c, drivers, "c")
	if !ok {
		t.Fatal("expected an assignment")
	}
	if got != "d2" {
		t.Fatalf("expected nearest driver d2, got %s", got)
	}
	if route.Distance != 1 {
		t.Fatalf("expected route distance 1, got %v", route.Distance)
	}
}

func TestBaselineSkipsNonIdle(t *testing.T) {
	c := lineCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"d1": driver("d1", "b", domain.DriverDelivering), // nearest but busy
		"d2": driver("d2", "a", domain.DriverIdle),       // farther but free
	}
	got, _, ok := Baseline(c, drivers, "c")
	if !ok {
		t.Fatal("expected an assignment")
	}
	if got != "d2" {
		t.Fatalf("expected the idle driver d2, got %s", got)
	}
}

func TestBaselineTieBreaksByID(t *testing.T) {
	c := lineCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"d2": driver("d2", "b", domain.DriverIdle),
		"d1": driver("d1", "b", domain.DriverIdle), // equal distance, lower ID
	}
	got, _, ok := Baseline(c, drivers, "c")
	if !ok {
		t.Fatal("expected an assignment")
	}
	if got != "d1" {
		t.Fatalf("expected the lower ID d1 to win the tie, got %s", got)
	}
}

func TestBaselineNoIdleDrivers(t *testing.T) {
	c := lineCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"d1": driver("d1", "a", domain.DriverDelivering),
		"d2": driver("d2", "b", domain.DriverAssigned),
	}
	if _, _, ok := Baseline(c, drivers, "c"); ok {
		t.Fatal("expected no assignment when no driver is idle")
	}
}

func TestBaselineNoReachableDriver(t *testing.T) {
	c := lineCity()
	drivers := map[domain.DriverID]*domain.Driver{
		"d1": driver("d1", "a", domain.DriverIdle),
	}
	// d is isolated, so no idle driver can route to it
	if _, _, ok := Baseline(c, drivers, "d"); ok {
		t.Fatal("expected no assignment when pickup is unreachable")
	}
}
