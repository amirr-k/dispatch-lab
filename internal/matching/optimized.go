package matching

import (
	"sort"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/routing"
	"dispatchlab/internal/spatial"
)

// Assignment is one accepted driver-order pairing from a batch matching
// run. The destination leg isn't included since it's computed uniformly
// downstream regardless of which strategy produced the pairing.
type Assignment struct {
	OrderID  domain.OrderID
	DriverID domain.DriverID
	ToPickup routing.Route
}

// CostWeights configures how pickup distance, order wait time, and driver
// idle time combine into a single cost per candidate pair. Visible and
// tunable so comparison mode can show exactly how the optimized assignment
// trades these off against each other, per the spec's requirement.
type CostWeights struct {
	// PickupDistance weights routed distance from driver to pickup.
	PickupDistance float64
	// WaitTime rewards (lowers cost for) orders that have waited longer,
	// so old orders aren't perpetually passed over for closer new ones.
	WaitTime float64
	// DriverIdleTime rewards drivers that have been idle longer, spreading
	// work more evenly instead of always reusing the same nearest driver -
	// a fairness signal baseline has no notion of at all.
	DriverIdleTime float64
}

// DefaultCostWeights favors pickup distance, matching the intuitive
// "nearest driver" behavior, with smaller nudges toward serving old orders
// and idle drivers first.
func DefaultCostWeights() CostWeights {
	return CostWeights{PickupDistance: 1, WaitTime: 1, DriverIdleTime: 0.1}
}

// Optimized assigns a batch of pending orders to idle drivers by solving
// min-cost bipartite assignment over a bounded per-order candidate set from
// the spatial index, rather than matching orders one at a time
// nearest-first like Baseline. Returns the accepted assignments, the ids of
// orders for which no candidate driver was reachable at all (a genuinely
// infeasible pairing, as distinct from an order that simply lost this
// round's competition and can be retried next batch), and the batch's total
// compute time.
func Optimized(
	city *domain.City,
	drivers map[domain.DriverID]*domain.Driver,
	orders []*domain.Order,
	index *spatial.Grid,
	candidatesPerOrder int,
	weights CostWeights,
	now float64,
) ([]Assignment, []domain.OrderID, float64) {
	start := time.Now()
	if len(orders) == 0 {
		return nil, nil, 0
	}

	sorted := make([]*domain.Order, len(orders))
	copy(sorted, orders)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	// union of every order's candidate drivers becomes the cost matrix's
	// (deterministically ordered) column set
	candidateSet := make(map[domain.DriverID]bool)
	perOrderCandidates := make([][]domain.DriverID, len(sorted))
	for i, o := range sorted {
		pickup := city.Nodes[o.Pickup]
		for _, id := range index.Candidates(spatial.Point{X: pickup.X, Y: pickup.Y}, candidatesPerOrder) {
			did := domain.DriverID(id)
			perOrderCandidates[i] = append(perOrderCandidates[i], did)
			candidateSet[did] = true
		}
	}

	columns := make([]domain.DriverID, 0, len(candidateSet))
	for id := range candidateSet {
		columns = append(columns, id)
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i] < columns[j] })
	colIndex := make(map[domain.DriverID]int, len(columns))
	for i, id := range columns {
		colIndex[id] = i
	}

	routes := make([]map[domain.DriverID]routing.Route, len(sorted))
	cost := make([][]float64, len(sorted))
	for i, o := range sorted {
		cost[i] = make([]float64, len(columns))
		for j := range cost[i] {
			cost[i][j] = Unreachable
		}
		routes[i] = make(map[domain.DriverID]routing.Route)

		for _, did := range perOrderCandidates[i] {
			d := drivers[did]
			route, ok := routing.FindRoute(city, d.Position, o.Pickup)
			if !ok {
				continue
			}
			routes[i][did] = route
			waitBonus := weights.WaitTime * (now - o.CreatedAtVirtualTime)
			idleBonus := weights.DriverIdleTime * (now - d.IdleSince)
			cost[i][colIndex[did]] = weights.PickupDistance*route.Distance - waitBonus - idleBonus
		}
	}

	pairs := MinCostAssignment(cost)
	computeMs := float64(time.Since(start).Microseconds()) / 1000.0

	assignedRows := make(map[int]bool, len(pairs))
	assigned := make([]Assignment, 0, len(pairs))
	for _, p := range pairs {
		if p.Cost >= Unreachable {
			continue
		}
		assignedRows[p.Row] = true
		driverID := columns[p.Col]
		assigned = append(assigned, Assignment{
			OrderID:  sorted[p.Row].ID,
			DriverID: driverID,
			ToPickup: routes[p.Row][driverID],
		})
	}

	// A row only counts as genuinely infeasible if it actually had
	// candidates to try and none of them could route there (e.g. an
	// isolated pickup). An order with zero candidates simply means no idle
	// driver exists to consider it *right now* - a transient condition
	// that resolves once one frees up, not a permanent one - so it's left
	// out of both lists and stays pending for the next batch window.
	var infeasible []domain.OrderID
	for i, o := range sorted {
		if !assignedRows[i] && len(perOrderCandidates[i]) > 0 && rowIsInfeasible(cost[i]) {
			infeasible = append(infeasible, o.ID)
		}
	}

	return assigned, infeasible, computeMs
}

func rowIsInfeasible(row []float64) bool {
	for _, c := range row {
		if c < Unreachable {
			return false
		}
	}
	return true
}
