package ws

import (
	"log"
	"net/http"

	"dispatchlab/internal/domain"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// the vertical slice serves its own frontend origin only; this is
	// revisited once the frontend is deployed separately in phase 9.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Snapshotter supplies a current-state snapshot for a reconnecting client.
type Snapshotter interface {
	CurrentSnapshot() domain.Event
}

// Lookup resolves a simulation id to its fanout hub and snapshot source.
type Lookup func(id string) (*Hub, Snapshotter, bool)

// Handler streams one simulation's events to a browser. On connect it sends a
// current snapshot and then only events newer than that snapshot, so a client
// that reconnects resumes cleanly from a known sequence with no gap or
// duplicate.
func Handler(lookup Lookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hub, snap, ok := lookup(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("ws upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		// detect client disconnect: a failed read closes the conn, which makes
		// the write loop below fail and unwind.
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					conn.Close()
					return
				}
			}
		}()

		// subscribe before snapshotting so no event emitted in between is lost;
		// events at or below the snapshot's sequence are already reflected in it.
		sub := hub.Subscribe()
		defer hub.Unsubscribe(sub)

		snapshot := snap.CurrentSnapshot()
		if err := conn.WriteJSON(snapshot); err != nil {
			return
		}
		lastSeq := snapshot.Sequence

		for event := range sub {
			if event.Sequence <= lastSeq && event.Type != domain.EventSimulationSnapshot {
				continue
			}
			if event.Sequence > lastSeq {
				lastSeq = event.Sequence
			}
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		}
	}
}
