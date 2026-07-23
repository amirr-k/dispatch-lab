package domain

// OrderStatus is the lifecycle state of an order.
type OrderStatus string

const (
	OrderPending      OrderStatus = "pending"
	OrderAssigned     OrderStatus = "assigned"
	OrderEnRoute      OrderStatus = "en_route"
	OrderDelivered    OrderStatus = "delivered"
	OrderUnassignable OrderStatus = "unassignable"
)

// OrderID identifies an order within a simulation.
type OrderID string

// Order is a delivery request from a pickup node to a destination node.
type Order struct {
	ID                   OrderID
	Pickup               NodeID
	Destination          NodeID
	CreatedAtVirtualTime float64
	Status               OrderStatus
	AssignedDriver       DriverID
}
