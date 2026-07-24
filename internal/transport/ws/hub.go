// Package ws streams simulation events to browsers over WebSocket.
package ws

import "dispatchlab/internal/domain"

// Hub fans a single simulation's event stream out to any number of
// subscribed connections. It owns no simulation state itself — it only
// relays what the simulation goroutine already decided.
type Hub struct {
	subscribe   chan chan domain.Event
	unsubscribe chan chan domain.Event
}

// NewHub starts fanning out events from source until source closes.
func NewHub(source <-chan domain.Event) *Hub {
	h := &Hub{
		subscribe:   make(chan chan domain.Event),
		unsubscribe: make(chan chan domain.Event),
	}
	go h.run(source)
	return h
}

func (h *Hub) run(source <-chan domain.Event) {
	subscribers := map[chan domain.Event]bool{}

	for {
		select {
		case sub := <-h.subscribe:
			subscribers[sub] = true
		case sub := <-h.unsubscribe:
			if subscribers[sub] {
				delete(subscribers, sub)
				close(sub)
			}
		case event, ok := <-source:
			if !ok {
				for sub := range subscribers {
					close(sub)
				}
				return
			}
			for sub := range subscribers {
				select {
				case sub <- event:
				default:
					// slow subscriber: drop this event rather than block the hub.
				}
			}
		}
	}
}

// Subscribe returns a channel receiving every event emitted from now on.
func (h *Hub) Subscribe() chan domain.Event {
	sub := make(chan domain.Event, 64)
	h.subscribe <- sub
	return sub
}

// Unsubscribe stops delivery to a channel previously returned by Subscribe.
func (h *Hub) Unsubscribe(sub chan domain.Event) {
	h.unsubscribe <- sub
}
