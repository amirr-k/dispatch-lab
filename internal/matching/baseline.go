// Package matching assigns orders to drivers.
package matching

import (
	"sort"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/routing"
)

// Baseline assigns the nearest idle driver to an order, by route distance.
// Drivers are considered in a deterministic order (sorted by ID) so ties
// resolve the same way on every run with the same input.
func Baseline(city *domain.City, drivers map[domain.DriverID]*domain.Driver, pickup domain.NodeID) (domain.DriverID, routing.Route, bool) {
	return baselineExcluding(city, drivers, pickup, nil)
}

// baselineExcluding is Baseline plus a set of driver ids to skip, so
// BaselineBatch can run it repeatedly within one batch without a driver
// already claimed earlier in that same batch being picked again before the
// batch's assignments are actually applied to real driver state.
func baselineExcluding(city *domain.City, drivers map[domain.DriverID]*domain.Driver, pickup domain.NodeID, exclude map[domain.DriverID]bool) (domain.DriverID, routing.Route, bool) {
	ids := make([]domain.DriverID, 0, len(drivers))
	for id, d := range drivers {
		if d.Status == domain.DriverIdle && !exclude[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var best domain.DriverID
	var bestRoute routing.Route
	found := false

	for _, id := range ids {
		d := drivers[id]
		route, ok := routing.FindRoute(city, d.Position, pickup)
		if !ok {
			continue
		}
		if !found || route.Distance < bestRoute.Distance {
			best, bestRoute, found = id, route, true
		}
	}

	return best, bestRoute, found
}

// BaselineBatch applies Baseline to a batch of orders, processed in
// order-arrival order (sorted by order ID, which is assigned in arrival
// order) — the same "nearest available driver" strategy as Baseline, just
// run over a batch so it can be compared against Optimized on an identical
// set of pending orders. Orders with no reachable idle driver are left out
// of the result, same contract as Baseline.
func BaselineBatch(city *domain.City, drivers map[domain.DriverID]*domain.Driver, orders []*domain.Order) ([]Assignment, float64) {
	start := time.Now()

	sorted := make([]*domain.Order, len(orders))
	copy(sorted, orders)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	taken := make(map[domain.DriverID]bool, len(sorted))
	result := make([]Assignment, 0, len(sorted))
	for _, o := range sorted {
		driverID, route, ok := baselineExcluding(city, drivers, o.Pickup, taken)
		if !ok {
			continue
		}
		taken[driverID] = true
		result = append(result, Assignment{OrderID: o.ID, DriverID: driverID, ToPickup: route})
	}

	return result, float64(time.Since(start).Microseconds()) / 1000.0
}
