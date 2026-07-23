// Package matching assigns orders to drivers.
package matching

import (
	"sort"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/routing"
)

// Baseline assigns the nearest idle driver to an order, by route distance.
// Drivers are considered in a deterministic order (sorted by ID) so ties
// resolve the same way on every run with the same input.
func Baseline(city *domain.City, drivers map[domain.DriverID]*domain.Driver, pickup domain.NodeID) (domain.DriverID, routing.Route, bool) {
	ids := make([]domain.DriverID, 0, len(drivers))
	for id, d := range drivers {
		if d.Status == domain.DriverIdle {
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
