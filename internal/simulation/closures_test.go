package simulation

import (
	"reflect"
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

// TestCloseRoadStampsCausationIDOnEveryEvent proves a command id set on the
// CloseRoad command rides along on every event it produces - the road.closed
// event itself and every route.invalidated/route.computed event the reroutes
// it triggers emit - so a caller can determine exactly which events a given
// closure caused without guessing from timing or event type.
func TestCloseRoadStampsCausationIDOnEveryEvent(t *testing.T) {
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

	const commandID = "cmd-test-123"
	evs := s.Apply(CloseRoad{EdgeID: edge.ID, CommandID: commandID})
	if len(evs) == 0 {
		t.Fatal("expected the closure to produce events")
	}
	for _, e := range evs {
		if e.CausationID != commandID {
			t.Fatalf("event %s: expected causationId %q, got %q", e.Type, commandID, e.CausationID)
		}
	}

	// currentCausationID must not leak into unrelated commands afterward.
	evs = s.Apply(SetSpeed{Multiplier: 2})
	for _, e := range evs {
		if e.CausationID != "" {
			t.Fatalf("expected no causationId on an unrelated command's event, got %q", e.CausationID)
		}
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

	// both directions count as used: closing a road shuts the edge and its
	// reverse together, so an edge whose reverse is on an active route is
	// not a safe "unused" candidate. Recording only the traveled direction
	// made this test intermittently pick one and fail (~2 runs in 25).
	used := map[[2]domain.NodeID]bool{}
	for _, d := range s.drivers {
		for i := 0; i < len(d.Route)-1; i++ {
			used[[2]domain.NodeID{d.Route[i], d.Route[i+1]}] = true
			used[[2]domain.NodeID{d.Route[i+1], d.Route[i]}] = true
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

func TestReopenRoadClearsBothDirections(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()

	edge := anyEdge(t, s.City)
	reverse, ok := edgeBetween(s.City, edge.To, edge.From)
	if !ok {
		t.Fatalf("expected a reverse edge for %s", edge.ID)
	}
	s.Apply(CloseRoad{EdgeID: edge.ID})

	evs := s.Apply(ReopenRoad{EdgeID: edge.ID})
	if len(evs) != 1 || evs[0].Type != domain.EventRoadReopened {
		t.Fatalf("expected a single road.reopened event, got %v", types(evs))
	}
	payload, ok := evs[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected a map payload, got %+v", evs[0].Payload)
	}
	ids, ok := payload["edgeIds"].([]domain.EdgeID)
	if !ok || len(ids) != 2 {
		t.Fatalf("expected both directions in edgeIds, got %+v", payload["edgeIds"])
	}

	if got, _ := s.City.EdgeByID(edge.ID); got.Closed {
		t.Error("forward edge is still closed")
	}
	if got, _ := s.City.EdgeByID(reverse.ID); got.Closed {
		t.Error("reverse edge is still closed")
	}
}

func TestReopenAlreadyOpenEdgeIsANoOp(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()

	edge := anyEdge(t, s.City)
	evs := s.Apply(ReopenRoad{EdgeID: edge.ID})
	if len(evs) != 0 {
		t.Fatalf("expected reopening an already-open edge to emit nothing, got %v", types(evs))
	}
}

func TestReopenUnknownEdgeIsANoOp(t *testing.T) {
	s := New("sim", scenarioSeed, scenarioDrivers)
	s.Start()
	evs := s.Apply(ReopenRoad{EdgeID: "does-not-exist"})
	if len(evs) != 0 {
		t.Fatalf("expected reopening an unknown edge to emit nothing, got %v", types(evs))
	}
}

// TestReopenDoesNotDisturbAnActiveRoute proves the doc comment on ReopenRoad:
// reopening a road a driver's current route does not use must not touch that
// route, since reopening only ever adds paths, never removes one already
// chosen.
func TestReopenDoesNotDisturbAnActiveRoute(t *testing.T) {
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
	routeBefore := append([]domain.NodeID{}, d.Route...)

	// close and reopen an edge nowhere on this driver's route.
	used := map[[2]domain.NodeID]bool{}
	for i := 0; i < len(d.Route)-1; i++ {
		used[[2]domain.NodeID{d.Route[i], d.Route[i+1]}] = true
		used[[2]domain.NodeID{d.Route[i+1], d.Route[i]}] = true
	}
	unused := anyUnusedEdge(t, s.City, used)

	s.Apply(CloseRoad{EdgeID: unused.ID})
	evs := s.Apply(ReopenRoad{EdgeID: unused.ID})
	for _, e := range evs {
		if e.Type == domain.EventRouteInvalidated || e.Type == domain.EventRouteComputed {
			t.Fatalf("reopening disturbed a route it never touched: %s", e.Type)
		}
	}
	if !reflect.DeepEqual(d.Route, routeBefore) {
		t.Fatalf("driver's route changed after an unrelated reopen: %v -> %v", routeBefore, d.Route)
	}
}

// TestCloseEdgeMakesRouteUnreachable uses a minimal 3-node line (a-b-c) with
// no alternate path, so closing the only edge to pickup leaves genuinely no
// route.
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

// anyUnusedEdge returns an edge whose (from, to) pair is not in used.
func anyUnusedEdge(t *testing.T, city *domain.City, used map[[2]domain.NodeID]bool) domain.Edge {
	t.Helper()
	for _, list := range city.Edges {
		for _, e := range list {
			if !used[[2]domain.NodeID{e.From, e.To}] {
				return e
			}
		}
	}
	t.Fatal("expected to find an edge not on any active route")
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
		commands: make(chan envelope, 8),
		events:   make(chan domain.Event, 64),
		queries:  make(chan chan domain.Event, 4),
		speed:    1,
	}
}
