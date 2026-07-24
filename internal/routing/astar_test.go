package routing

import (
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
)

// dijkstra is a deliberately simple reference implementation used to check A*
// returns optimal distances. O(V^2), fine for the small graphs under test.
func dijkstra(c *domain.City, start, goal domain.NodeID) (float64, bool) {
	if _, ok := c.Nodes[start]; !ok {
		return 0, false
	}
	if _, ok := c.Nodes[goal]; !ok {
		return 0, false
	}

	dist := make(map[domain.NodeID]float64, len(c.Nodes))
	for id := range c.Nodes {
		dist[id] = math.Inf(1)
	}
	dist[start] = 0
	visited := make(map[domain.NodeID]bool, len(c.Nodes))

	for {
		u, best, found := domain.NodeID(""), math.Inf(1), false
		for id, d := range dist {
			if !visited[id] && d < best {
				u, best, found = id, d, true
			}
		}
		if !found || u == goal {
			break
		}
		visited[u] = true
		for _, e := range c.Edges[u] {
			if e.Closed {
				continue
			}
			if nd := dist[u] + e.Weight; nd < dist[e.To] {
				dist[e.To] = nd
			}
		}
	}

	if math.IsInf(dist[goal], 1) {
		return 0, false
	}
	return dist[goal], true
}

// pathDistance walks a route and returns its measured length, or ok=false if
// any hop is not a real open edge.
func pathDistance(c *domain.City, nodes []domain.NodeID) (float64, bool) {
	total := 0.0
	for i := 0; i < len(nodes)-1; i++ {
		hop, ok := false, false
		for _, e := range c.Edges[nodes[i]] {
			if e.To == nodes[i+1] && !e.Closed {
				total += e.Weight
				hop, ok = true, true
				break
			}
		}
		if !hop || !ok {
			return 0, false
		}
	}
	return total, true
}

func sortedNodeIDs(c *domain.City) []domain.NodeID {
	ids := make([]domain.NodeID, 0, len(c.Nodes))
	for id := range c.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// TestAStarMatchesDijkstra is the property test the spec calls for: across
// many randomized small graphs (with random closures) A* must agree with
// Dijkstra on reachability and optimal distance, and its path must be valid.
func TestAStarMatchesDijkstra(t *testing.T) {
	for seed := 0; seed < 25; seed++ {
		c := GenerateGridForTest(int64(seed))
		rng := rand.New(rand.NewSource(int64(seed)*31 + 7))

		// close a fraction of edges to exercise reachability differences
		for from := range c.Edges {
			for i := range c.Edges[from] {
				if rng.Float64() < 0.12 {
					c.Edges[from][i].Closed = true
				}
			}
		}

		ids := sortedNodeIDs(c)
		for trial := 0; trial < 40; trial++ {
			start := ids[rng.Intn(len(ids))]
			goal := ids[rng.Intn(len(ids))]

			wantDist, wantOK := dijkstra(c, start, goal)
			route, gotOK := FindRoute(c, start, goal)

			if gotOK != wantOK {
				t.Fatalf("seed %d %s->%s: reachability A*=%v dijkstra=%v", seed, start, goal, gotOK, wantOK)
			}
			if !gotOK {
				continue
			}
			if math.Abs(route.Distance-wantDist) > 1e-6 {
				t.Fatalf("seed %d %s->%s: A* dist %v != optimal %v", seed, start, goal, route.Distance, wantDist)
			}
			if route.Nodes[0] != start || route.Nodes[len(route.Nodes)-1] != goal {
				t.Fatalf("seed %d: route does not run start->goal: %v", seed, route.Nodes)
			}
			measured, valid := pathDistance(c, route.Nodes)
			if !valid {
				t.Fatalf("seed %d: route traverses a missing or closed edge: %v", seed, route.Nodes)
			}
			if math.Abs(measured-route.Distance) > 1e-6 {
				t.Fatalf("seed %d: reported distance %v != summed path %v", seed, route.Distance, measured)
			}
		}
	}
}

func TestFindRouteStartEqualsGoal(t *testing.T) {
	c := GenerateGridForTest(1)
	ids := sortedNodeIDs(c)
	route, ok := FindRoute(c, ids[0], ids[0])
	if !ok {
		t.Fatal("start==goal should be reachable")
	}
	if route.Distance != 0 || len(route.Nodes) != 1 {
		t.Fatalf("expected zero-length single-node route, got %+v", route)
	}
}

func TestFindRouteUnreachable(t *testing.T) {
	c := &domain.City{
		Nodes: map[domain.NodeID]domain.Node{
			"a": {ID: "a"}, "b": {ID: "b"},
		},
		Edges: map[domain.NodeID][]domain.Edge{},
	}
	if _, ok := FindRoute(c, "a", "b"); ok {
		t.Fatal("disconnected nodes should be unreachable")
	}
}

func TestFindRouteMalformed(t *testing.T) {
	c := GenerateGridForTest(1)
	ids := sortedNodeIDs(c)
	if _, ok := FindRoute(c, "nope", ids[0]); ok {
		t.Fatal("missing start should return not found")
	}
	if _, ok := FindRoute(c, ids[0], "nope"); ok {
		t.Fatal("missing goal should return not found")
	}
}

func TestFindRouteAvoidsClosedEdge(t *testing.T) {
	// square: a-b-c along the top, a-d-c along the bottom, both length 2
	c := squareCity()
	c.SetClosed("e-b-c", true)

	route, ok := FindRoute(c, "a", "c")
	if !ok {
		t.Fatal("route should still exist via d")
	}
	for i := 0; i < len(route.Nodes)-1; i++ {
		if route.Nodes[i] == "b" && route.Nodes[i+1] == "c" {
			t.Fatal("route used the closed b->c edge")
		}
	}
}

func TestFindRouteDeterministic(t *testing.T) {
	c := squareCity()
	r1, ok1 := FindRoute(c, "a", "c")
	r2, ok2 := FindRoute(c, "a", "c")
	if !ok1 || !ok2 {
		t.Fatal("route should exist")
	}
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("A* not deterministic: %v vs %v", r1.Nodes, r2.Nodes)
	}
}

// squareCity builds a 4-node square where a->c has two equal-cost paths, used
// to exercise tie-breaking and closed-edge avoidance.
func squareCity() *domain.City {
	edge := func(from, to domain.NodeID) domain.Edge {
		return domain.Edge{ID: domain.EdgeID("e-" + string(from) + "-" + string(to)), From: from, To: to, Weight: 1}
	}
	return &domain.City{
		Nodes: map[domain.NodeID]domain.Node{
			"a": {ID: "a", X: 0, Y: 0},
			"b": {ID: "b", X: 1, Y: 1},
			"c": {ID: "c", X: 2, Y: 0},
			"d": {ID: "d", X: 1, Y: -1},
		},
		Edges: map[domain.NodeID][]domain.Edge{
			"a": {edge("a", "b"), edge("a", "d")},
			"b": {edge("b", "a"), edge("b", "c")},
			"c": {edge("c", "b"), edge("c", "d")},
			"d": {edge("d", "a"), edge("d", "c")},
		},
	}
}

// GenerateGridForTest wraps the city generator with a fixed jittered layout so
// routing tests exercise realistic weighted graphs.
func GenerateGridForTest(seed int64) *domain.City {
	return city.GenerateGrid(city.GridConfig{Seed: seed, Rows: 5, Cols: 5, CellSpacing: 100, JitterFraction: 0.2})
}
