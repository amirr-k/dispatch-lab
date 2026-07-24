package simulation

import (
	"testing"

	"dispatchlab/internal/domain"
)

func TestCloseUsedEdgeReroutesDriver(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	var d *domain.Driver
	for _, dr := range s.drivers {
		if len(dr.Route) > 0 {
			d = dr
			break
		}
	}
	if d == nil {
		t.Fatal("expected an assigned driver with a route")
	}

	from, to := d.Route[d.RouteIndex], d.Route[d.RouteIndex+1]
	edge, ok := edgeBetween(s.City, from, to)
	if !ok {
		t.Fatalf("expected an edge between %s and %s", from, to)
	}

	evs := s.Apply(CloseRoad{EdgeID: edge.ID})

	var sawInvalidated, sawComputed bool
	var closedPayload map[string]any
	for _, e := range evs {
		switch e.Type {
		case domain.EventRouteInvalidated:
			sawInvalidated = true
		case domain.EventRouteComputed:
			sawComputed = true
		case domain.EventRoadClosed:
			closedPayload = e.Payload.(map[string]any)
		}
	}
	if !sawInvalidated || !sawComputed || closedPayload == nil {
		t.Fatalf("expected invalidated+computed+closed events, got %v", types(evs))
	}
	if closedPayload["affectedRoutes"] != 1 {
		t.Fatalf("expected 1 affected route, got %v", closedPayload["affectedRoutes"])
	}
	if routeUsesClosedEdge(s.City, d.Route, 0) {
		t.Fatal("rerouted driver's new route still crosses a closed edge")
	}
}

func TestCloseRoadClosesBothDirections(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()

	edge := anyEdge(t, s.City)
	s.Apply(CloseRoad{EdgeID: edge.ID})

	got, _ := s.City.EdgeByID(edge.ID)
	if !got.Closed {
		t.Fatal("expected the closed edge to be marked closed")
	}
	reverse, ok := edgeBetween(s.City, edge.To, edge.From)
	if !ok || !reverse.Closed {
		t.Fatal("expected the reverse direction to also be closed")
	}
}

func TestCloseUnusedEdgeIsANoOp(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})

	used := map[[2]domain.NodeID]bool{}
	for _, d := range s.drivers {
		for i := 0; i < len(d.Route)-1; i++ {
			used[[2]domain.NodeID{d.Route[i], d.Route[i+1]}] = true
		}
	}

	var target domain.Edge
	found := false
	for _, list := range s.City.Edges {
		for _, e := range list {
			if !used[[2]domain.NodeID{e.From, e.To}] {
				target, found = e, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("expected to find an edge not on any active route")
	}

	evs := s.Apply(CloseRoad{EdgeID: target.ID})
	for _, e := range evs {
		if e.Type == domain.EventRouteInvalidated || e.Type == domain.EventRouteComputed || e.Type == domain.EventOrderUnassignable {
			t.Fatalf("closing an unused edge should not touch any route, got %s", e.Type)
		}
		if e.Type == domain.EventRoadClosed {
			if p := e.Payload.(map[string]any); p["affectedRoutes"] != 0 {
				t.Fatalf("expected 0 affected routes, got %v", p["affectedRoutes"])
			}
		}
	}
}

func TestCloseAlreadyClosedEdgeIsANoOp(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()

	edge := anyEdge(t, s.City)
	s.Apply(CloseRoad{EdgeID: edge.ID})
	evs := s.Apply(CloseRoad{EdgeID: edge.ID})
	if len(evs) != 0 {
		t.Fatalf("expected closing an already-closed edge to emit nothing, got %v", types(evs))
	}
}

func TestCloseUnknownEdgeIsANoOp(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	evs := s.Apply(CloseRoad{EdgeID: "does-not-exist"})
	if len(evs) != 0 {
		t.Fatalf("expected closing an unknown edge to emit nothing, got %v", types(evs))
	}
}

// TestCloseEdgeMakesRouteUnreachable uses a minimal 3-node line (a-b-c) with
// no alternate path, so closing the only edge to pickup leaves genuinely no
// route — the explicit unreachable result the exit gate calls for.
func TestCloseEdgeMakesRouteUnreachable(t *testing.T) {
	city := lineCityForClosureTest()
	drivers := map[domain.DriverID]*domain.Driver{
		"d1": {ID: "d1", Position: "a", Status: domain.DriverIdle},
	}
	s := newTestSimulation(city, drivers)
	s.Start()

	s.Apply(PlaceOrder{Pickup: "b", Destination: "c"})
	d := s.drivers["d1"]
	if d.Status != domain.DriverEnRouteToPick {
		t.Fatalf("expected driver to be en route to pickup, got %s", d.Status)
	}

	edge, ok := edgeBetween(city, "a", "b")
	if !ok {
		t.Fatal("expected edge a->b to exist")
	}

	evs := s.Apply(CloseRoad{EdgeID: edge.ID})

	var sawUnassignable bool
	for _, e := range evs {
		if e.Type == domain.EventOrderUnassignable {
			sawUnassignable = true
		}
	}
	if !sawUnassignable {
		t.Fatalf("expected order.unassignable once closure left no path, got %v", types(evs))
	}
	if d.Status != domain.DriverIdle || len(d.Route) != 0 {
		t.Fatalf("expected driver freed back to idle with no route, got status=%s route=%v", d.Status, d.Route)
	}

	order := s.orders["order-1"]
	if order.Status != domain.OrderUnassignable {
		t.Fatalf("expected order status unassignable, got %s", order.Status)
	}
}

// FuzzCloseRoad throws arbitrary edge identifiers at a running simulation
// with an active order. A garbage id must never panic or corrupt state.
func FuzzCloseRoad(f *testing.F) {
	f.Add("e-n-0-0-n-0-1")
	f.Add("")
	f.Add("does-not-exist")
	f.Add("💥")

	f.Fuzz(func(t *testing.T, edgeID string) {
		s := New("fuzz-closure", 1, 4)
		s.Start()
		s.Apply(PlaceOrder{Pickup: scenarioPickup, Destination: scenarioDest})
		s.Apply(CloseRoad{EdgeID: domain.EdgeID(edgeID)})
		for i := 0; i < 20; i++ {
			s.Advance()
			checkInvariants(t, s)
		}
	})
}

func anyEdge(t *testing.T, city *domain.City) domain.Edge {
	t.Helper()
	for _, list := range city.Edges {
		if len(list) > 0 {
			return list[0]
		}
	}
	t.Fatal("expected the city to have at least one edge")
	return domain.Edge{}
}

func lineCityForClosureTest() *domain.City {
	mk := func(from, to domain.NodeID) domain.Edge {
		return domain.Edge{ID: domain.EdgeID("e-" + string(from) + "-" + string(to)), From: from, To: to, Weight: 1}
	}
	return &domain.City{
		Nodes: map[domain.NodeID]domain.Node{
			"a": {ID: "a", X: 0, Y: 0},
			"b": {ID: "b", X: 1, Y: 0},
			"c": {ID: "c", X: 2, Y: 0},
		},
		Edges: map[domain.NodeID][]domain.Edge{
			"a": {mk("a", "b")},
			"b": {mk("b", "a"), mk("b", "c")},
			"c": {mk("c", "b")},
		},
	}
}

func newTestSimulation(city *domain.City, drivers map[domain.DriverID]*domain.Driver) *Simulation {
	return &Simulation{
		ID:       "closure-test",
		City:     city,
		drivers:  drivers,
		orders:   make(map[domain.OrderID]*domain.Order),
		commands: make(chan Command, 8),
		events:   make(chan domain.Event, 64),
		queries:  make(chan chan domain.Event, 4),
		speed:    1,
	}
}
