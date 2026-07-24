// Package replay reconstructs what a simulation looked like at any point in
// its event log, and records live simulations into the store so that log
// exists in the first place.
package replay

import (
	"encoding/json"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/store"
)

// Node, Edge, Driver, and Order mirror the simulation's snapshot payload.
// Replay decodes a snapshot into these, folds the events after it forward,
// and hands back the same shape, so a client renders a reconstructed frame
// with exactly the code it uses for a live one.
type Node struct {
	ID domain.NodeID `json:"id"`
	X  float64       `json:"x"`
	Y  float64       `json:"y"`
}

type Edge struct {
	ID     domain.EdgeID `json:"id"`
	From   domain.NodeID `json:"from"`
	To     domain.NodeID `json:"to"`
	Closed bool          `json:"closed"`
}

type Driver struct {
	ID            domain.DriverID     `json:"id"`
	Position      domain.NodeID       `json:"position"`
	Status        domain.DriverStatus `json:"status"`
	Route         []domain.NodeID     `json:"route,omitempty"`
	RouteIndex    int                 `json:"routeIndex"`
	AssignedOrder domain.OrderID      `json:"assignedOrder,omitempty"`
}

type Order struct {
	ID                   domain.OrderID     `json:"id"`
	Pickup               domain.NodeID      `json:"pickup"`
	Destination          domain.NodeID      `json:"destination"`
	Status               domain.OrderStatus `json:"status"`
	AssignedDriver       domain.DriverID    `json:"assignedDriver,omitempty"`
	CreatedAtVirtualTime float64            `json:"createdAtVirtualTime"`
}

// State is simulation state as of a particular sequence number.
type State struct {
	SimulationID string   `json:"simulationId"`
	Sequence     int      `json:"sequence"`
	VirtualTime  float64  `json:"virtualTime"`
	Nodes        []Node   `json:"nodes"`
	Edges        []Edge   `json:"edges"`
	Drivers      []Driver `json:"drivers"`
	Orders       []Order  `json:"orders"`
	Paused       bool     `json:"paused"`
	Speed        float64  `json:"speed"`
}

// snapshotPayload is the wire form of a simulation.snapshot payload.
type snapshotPayload struct {
	Nodes   []Node   `json:"nodes"`
	Edges   []Edge   `json:"edges"`
	Drivers []Driver `json:"drivers"`
	Orders  []Order  `json:"orders"`
	Paused  bool     `json:"paused"`
	Speed   float64  `json:"speed"`
}

// stateBuilder folds events onto a decoded snapshot. Drivers and orders are
// kept in maps while folding and flattened back into the ordered slices the
// snapshot arrived in, so the output stays stable regardless of the order
// events touched them.
type stateBuilder struct {
	simulationID string
	sequence     int
	virtualTime  float64
	nodes        []Node
	edges        []Edge
	driverOrder  []domain.DriverID
	drivers      map[domain.DriverID]*Driver
	orderOrder   []domain.OrderID
	orders       map[domain.OrderID]*Order
	paused       bool
	speed        float64
}

func newStateBuilder(simulationID string) *stateBuilder {
	return &stateBuilder{
		simulationID: simulationID,
		drivers:      make(map[domain.DriverID]*Driver),
		orders:       make(map[domain.OrderID]*Order),
		speed:        1,
	}
}

// StateAt reconstructs simulation state at targetSequence from a base
// snapshot plus the events that followed it. Events beyond the target are
// ignored, so the same inputs can serve any point on a scrubber. A zero or
// negative target means "fold everything given".
func StateAt(simulationID string, base *store.Snapshot, events []store.Event, targetSequence int) (State, error) {
	b := newStateBuilder(simulationID)

	if base != nil {
		if err := b.applySnapshot(base.Payload); err != nil {
			return State{}, err
		}
		b.sequence = base.Sequence
		b.virtualTime = base.VirtualTime
	}

	for _, e := range events {
		if targetSequence > 0 && e.Sequence > targetSequence {
			break
		}
		if base != nil && e.Sequence <= base.Sequence {
			continue
		}
		if err := b.apply(e); err != nil {
			return State{}, err
		}
	}
	return b.state(), nil
}

func (b *stateBuilder) applySnapshot(payload json.RawMessage) error {
	var snap snapshotPayload
	if err := json.Unmarshal(payload, &snap); err != nil {
		return err
	}

	b.nodes = snap.Nodes
	b.edges = snap.Edges
	b.paused = snap.Paused
	b.speed = snap.Speed
	if b.speed == 0 {
		b.speed = 1
	}

	b.driverOrder = b.driverOrder[:0]
	b.drivers = make(map[domain.DriverID]*Driver, len(snap.Drivers))
	for i := range snap.Drivers {
		d := snap.Drivers[i]
		b.driverOrder = append(b.driverOrder, d.ID)
		b.drivers[d.ID] = &d
	}

	b.orderOrder = b.orderOrder[:0]
	b.orders = make(map[domain.OrderID]*Order, len(snap.Orders))
	for i := range snap.Orders {
		o := snap.Orders[i]
		b.orderOrder = append(b.orderOrder, o.ID)
		b.orders[o.ID] = &o
	}
	return nil
}

// apply folds one event into the builder. Payloads are decoded into the
// narrow struct each event type actually carries rather than a generic map,
// so a renamed field fails at the boundary instead of silently reconstructing
// a wrong frame.
func (b *stateBuilder) apply(e store.Event) error {
	b.sequence = e.Sequence
	b.virtualTime = e.VirtualTime

	switch e.Type {
	case domain.EventSimulationSnapshot:
		return b.applySnapshot(e.Payload)

	case domain.EventOrderPlaced:
		var p struct {
			OrderID     domain.OrderID `json:"orderId"`
			Pickup      domain.NodeID  `json:"pickupNodeId"`
			Destination domain.NodeID  `json:"destinationNodeId"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		b.order(p.OrderID).Pickup = p.Pickup
		b.order(p.OrderID).Destination = p.Destination
		b.order(p.OrderID).Status = domain.OrderPending
		b.order(p.OrderID).CreatedAtVirtualTime = e.VirtualTime

	case domain.EventOrderAssigned:
		var p struct {
			OrderID  domain.OrderID  `json:"orderId"`
			DriverID domain.DriverID `json:"driverId"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		order := b.order(p.OrderID)
		order.Status = domain.OrderAssigned
		order.AssignedDriver = p.DriverID
		b.driver(p.DriverID).AssignedOrder = p.OrderID

	case domain.EventOrderUnassignable:
		var p struct {
			OrderID domain.OrderID `json:"orderId"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		b.order(p.OrderID).Status = domain.OrderUnassignable

	case domain.EventOrderDelivered:
		var p struct {
			OrderID domain.OrderID `json:"orderId"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		b.order(p.OrderID).Status = domain.OrderDelivered

	case domain.EventDriverPositionUpdate:
		var p struct {
			DriverID domain.DriverID `json:"driverId"`
			NodeID   domain.NodeID   `json:"nodeId"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		d := b.driver(p.DriverID)
		d.Position = p.NodeID
		if d.RouteIndex+1 < len(d.Route) && d.Route[d.RouteIndex+1] == p.NodeID {
			d.RouteIndex++
		}

	case domain.EventDriverStatusChanged:
		var p struct {
			DriverID domain.DriverID     `json:"driverId"`
			Status   domain.DriverStatus `json:"status"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		d := b.driver(p.DriverID)
		d.Status = p.Status
		switch p.Status {
		case domain.DriverDelivering:
			// the pickup itself has no event of its own; a driver switching to
			// delivering is what puts its order en route.
			if d.AssignedOrder != "" {
				b.order(d.AssignedOrder).Status = domain.OrderEnRoute
			}
		case domain.DriverIdle:
			d.Route = nil
			d.RouteIndex = 0
			d.AssignedOrder = ""
		}

	case domain.EventRouteComputed:
		var p struct {
			DriverID domain.DriverID `json:"driverId"`
			NodeIDs  []domain.NodeID `json:"nodeIds"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		d := b.driver(p.DriverID)
		d.Route = p.NodeIDs
		d.RouteIndex = 0

	case domain.EventRoadClosed, domain.EventRoadReopened:
		var p struct {
			EdgeIDs []domain.EdgeID `json:"edgeIds"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		closed := e.Type == domain.EventRoadClosed
		for _, id := range p.EdgeIDs {
			for i := range b.edges {
				if b.edges[i].ID == id {
					b.edges[i].Closed = closed
				}
			}
		}

	case domain.EventSimulationPaused:
		var p struct {
			Paused bool `json:"paused"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		b.paused = p.Paused

	case domain.EventSimulationSpeed:
		var p struct {
			Multiplier float64 `json:"multiplier"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		b.speed = p.Multiplier
	}

	return nil
}

func (b *stateBuilder) driver(id domain.DriverID) *Driver {
	d, ok := b.drivers[id]
	if !ok {
		d = &Driver{ID: id}
		b.drivers[id] = d
		b.driverOrder = append(b.driverOrder, id)
	}
	return d
}

func (b *stateBuilder) order(id domain.OrderID) *Order {
	o, ok := b.orders[id]
	if !ok {
		o = &Order{ID: id}
		b.orders[id] = o
		b.orderOrder = append(b.orderOrder, id)
	}
	return o
}

func (b *stateBuilder) state() State {
	drivers := make([]Driver, 0, len(b.driverOrder))
	for _, id := range b.driverOrder {
		drivers = append(drivers, *b.drivers[id])
	}
	orders := make([]Order, 0, len(b.orderOrder))
	for _, id := range b.orderOrder {
		orders = append(orders, *b.orders[id])
	}

	return State{
		SimulationID: b.simulationID,
		Sequence:     b.sequence,
		VirtualTime:  b.virtualTime,
		Nodes:        b.nodes,
		Edges:        b.edges,
		Drivers:      drivers,
		Orders:       orders,
		Paused:       b.paused,
		Speed:        b.speed,
	}
}
