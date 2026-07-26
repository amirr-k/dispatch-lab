package domain

// DriverStatus is the lifecycle state of a driver within a simulation.
type DriverStatus string

const (
	DriverIdle          DriverStatus = "idle"
	DriverAssigned      DriverStatus = "assigned"
	DriverEnRouteToPick DriverStatus = "en_route_to_pickup"
	DriverDelivering    DriverStatus = "delivering"
	DriverUnavailable   DriverStatus = "unavailable"
)

// DriverID identifies a driver within a simulation.
type DriverID string

// Driver is a delivery driver moving through the city graph.
type Driver struct {
	ID       DriverID
	Position NodeID
	Status   DriverStatus
	// Route is the sequence of nodes the driver is currently traversing, if any.
	Route []NodeID
	// RouteIndex is the position of Position within Route.
	RouteIndex    int
	AssignedOrder OrderID
	// IdleSince is the virtual time this driver last became idle. Used by
	// optimized matching's driver-state cost term to favor drivers that
	// have been waiting longest (utilization fairness), which greedy
	// nearest-driver baseline matching has no notion of.
	IdleSince float64
	// RouteProgress is how far the driver has travelled along the current
	// edge from Route[RouteIndex] toward Route[RouteIndex+1]. Used to render
	// drivers between intersections and to make longer roads genuinely take
	// more virtual time.
	RouteProgress float64
}
