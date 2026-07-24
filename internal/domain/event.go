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
// JSON tags match the wire envelope in docs/api-schema.yaml.
type Event struct {
	SchemaVersion int       `json:"schemaVersion"`
	SimulationID  string    `json:"simulationId"`
	Sequence      int       `json:"sequence"`
	VirtualTime   float64   `json:"virtualTime"`
	Type          EventType `json:"type"`
	Payload       any       `json:"payload"`
}
