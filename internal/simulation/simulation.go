// Package simulation runs one deterministic simulation as an actor: a single
// goroutine owns all mutable state, commands enter through a bounded
// channel, and immutable events are the only output.
package simulation

import (
	"context"
	"fmt"
	"sort"
	"time"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
	"dispatchlab/internal/matching"
	"dispatchlab/internal/routing"
)

// tickInterval is wall-clock only: it paces how fast virtual time is
// advanced and flushed for a live viewer. It never affects what happens.
const tickInterval = 500 * time.Millisecond

// virtualStepPerTick is the deterministic amount of virtual time each tick
// advances, independent of actual wall-clock elapsed since the last tick.
const virtualStepPerTick = 1.0

// PlaceOrder is a command to create an order and trigger assignment.
type PlaceOrder struct {
	Pickup      domain.NodeID
	Destination domain.NodeID
}

// Simulation owns one simulation's state and runs its actor loop.
type Simulation struct {
	ID     string
	Seed   int64
	City   *domain.City
	Status string // running | paused | completed

	drivers map[domain.DriverID]*domain.Driver
	orders  map[domain.OrderID]*domain.Order

	virtualTime float64
	sequence    int

	commands chan PlaceOrder
	events   chan domain.Event

	nextOrderID int
}

// New builds a simulation with a deterministically generated small city and
// driverCount drivers placed at deterministic starting nodes.
func New(id string, seed int64, driverCount int) *Simulation {
	c := city.GenerateGrid(city.DefaultGridConfig(seed))

	nodeIDs := make([]domain.NodeID, 0, len(c.Nodes))
	for nid := range c.Nodes {
		nodeIDs = append(nodeIDs, nid)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	drivers := make(map[domain.DriverID]*domain.Driver, driverCount)
	for i := 0; i < driverCount && i < len(nodeIDs); i++ {
		did := domain.DriverID(shortID("driver", i))
		drivers[did] = &domain.Driver{
			ID:       did,
			Position: nodeIDs[i*len(nodeIDs)/driverCount],
			Status:   domain.DriverIdle,
		}
	}

	return &Simulation{
		ID:       id,
		Seed:     seed,
		City:     c,
		Status:   "running",
		drivers:  drivers,
		orders:   make(map[domain.OrderID]*domain.Order),
		commands: make(chan PlaceOrder, 32),
		events:   make(chan domain.Event, 256),
	}
}

func shortID(prefix string, i int) string {
	return fmt.Sprintf("%s-%d", prefix, i)
}

// Events returns the read-only stream of events this simulation emits.
func (s *Simulation) Events() <-chan domain.Event {
	return s.events
}

// Submit enqueues a command for the simulation's actor loop. It never
// blocks the caller on simulation progress beyond the channel's capacity.
func (s *Simulation) Submit(cmd PlaceOrder) {
	s.commands <- cmd
}

// Run is the actor loop: the only goroutine that ever mutates simulation
// state. It exits when ctx is canceled.
func (s *Simulation) Run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	defer close(s.events)

	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-s.commands:
			s.handlePlaceOrder(cmd)
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Simulation) emit(t domain.EventType, payload any) {
	s.sequence++
	s.events <- domain.Event{
		SchemaVersion: 1,
		SimulationID:  s.ID,
		Sequence:      s.sequence,
		VirtualTime:   s.virtualTime,
		Type:          t,
		Payload:       payload,
	}
}

func (s *Simulation) handlePlaceOrder(cmd PlaceOrder) {
	s.nextOrderID++
	orderID := domain.OrderID(shortID("order", s.nextOrderID))
	order := &domain.Order{
		ID:                   orderID,
		Pickup:               cmd.Pickup,
		Destination:          cmd.Destination,
		CreatedAtVirtualTime: s.virtualTime,
		Status:               domain.OrderPending,
	}
	s.orders[orderID] = order

	s.emit(domain.EventOrderPlaced, map[string]any{
		"orderId":           orderID,
		"pickupNodeId":      cmd.Pickup,
		"destinationNodeId": cmd.Destination,
	})

	start := time.Now()
	driverID, toPickup, ok := matching.Baseline(s.City, s.drivers, cmd.Pickup)
	computeMs := float64(time.Since(start).Microseconds()) / 1000.0

	if !ok {
		order.Status = domain.OrderUnassignable
		s.emit(domain.EventOrderUnassignable, map[string]any{
			"orderId": orderID,
			"reason":  "no available driver can reach the pickup",
		})
		return
	}

	toDestination, ok := routing.FindRoute(s.City, cmd.Pickup, cmd.Destination)
	if !ok {
		order.Status = domain.OrderUnassignable
		s.emit(domain.EventOrderUnassignable, map[string]any{
			"orderId": orderID,
			"reason":  "no path from pickup to destination",
		})
		return
	}

	driver := s.drivers[driverID]
	fullRoute := append(append([]domain.NodeID{}, toPickup.Nodes...), toDestination.Nodes[1:]...)
	driver.Route = fullRoute
	driver.RouteIndex = 0
	driver.Status = domain.DriverEnRouteToPick
	driver.AssignedOrder = orderID

	order.Status = domain.OrderAssigned
	order.AssignedDriver = driverID

	s.emit(domain.EventRouteComputed, map[string]any{
		"driverId": driverID,
		"nodeIds":  fullRoute,
		"distance": toPickup.Distance + toDestination.Distance,
	})
	s.emit(domain.EventOrderAssigned, map[string]any{
		"orderId":              orderID,
		"driverId":             driverID,
		"pickupEtaVirtualTime": s.virtualTime + toPickup.Distance,
		"assignmentComputeMs":  computeMs,
	})
	s.emit(domain.EventDriverStatusChanged, map[string]any{
		"driverId": driverID,
		"status":   driver.Status,
	})
}

// tick advances virtual time by a fixed deterministic step and moves every
// en-route driver one node forward along its route.
func (s *Simulation) tick() {
	s.virtualTime += virtualStepPerTick

	ids := make([]domain.DriverID, 0, len(s.drivers))
	for id := range s.drivers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		d := s.drivers[id]
		if len(d.Route) == 0 || d.RouteIndex >= len(d.Route)-1 {
			continue
		}
		d.RouteIndex++
		d.Position = d.Route[d.RouteIndex]

		s.emit(domain.EventDriverPositionUpdate, map[string]any{
			"driverId": id,
			"nodeId":   d.Position,
		})

		order, hasOrder := s.orders[d.AssignedOrder]
		if hasOrder && d.Status == domain.DriverEnRouteToPick && d.Position == order.Pickup {
			d.Status = domain.DriverDelivering
			order.Status = domain.OrderEnRoute
			s.emit(domain.EventDriverStatusChanged, map[string]any{"driverId": id, "status": d.Status})
		}

		if d.RouteIndex == len(d.Route)-1 {
			if hasOrder {
				order.Status = domain.OrderDelivered
				s.emit(domain.EventOrderDelivered, map[string]any{"orderId": order.ID, "driverId": id})
			}
			d.Status = domain.DriverIdle
			d.Route = nil
			d.RouteIndex = 0
			d.AssignedOrder = ""
			s.emit(domain.EventDriverStatusChanged, map[string]any{"driverId": id, "status": d.Status})
		}
	}
}
