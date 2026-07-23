package domain

// EventType names the kind of a simulation event, matching the values in
// docs/websocket-protocol.md.
type EventType string

const (
	EventSimulationSnapshot   EventType = "simulation.snapshot"
	EventOrderPlaced          EventType = "order.placed"
	EventOrderAssigned        EventType = "order.assigned"
	EventOrderUnassignable    EventType = "order.unassignable"
	EventOrderDelivered       EventType = "order.delivered"
	EventDriverPositionUpdate EventType = "driver.position.updated"
	EventDriverStatusChanged  EventType = "driver.status.changed"
	EventRouteComputed        EventType = "route.computed"
	EventRouteInvalidated     EventType = "route.invalidated"
	EventRoadClosed           EventType = "road.closed"
	EventRoadReopened         EventType = "road.reopened"
)

// Event is an immutable fact emitted by a simulation, ordered by Sequence.
type Event struct {
	SchemaVersion int
	SimulationID  string
	Sequence      int
	VirtualTime   float64
	Type          EventType
	Payload       any
}
