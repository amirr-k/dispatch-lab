package ws

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// the vertical slice serves its own frontend origin only; this is
	// revisited once the frontend is deployed separately in phase 9.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler upgrades a request to a WebSocket and streams every event the hub
// delivers as JSON until the client disconnects.
func Handler(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("ws upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		sub := hub.Subscribe()
		defer hub.Unsubscribe(sub)

		for event := range sub {
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		}
	}
}
