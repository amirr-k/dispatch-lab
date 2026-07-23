// Command server runs the DispatchLab backend: it starts one demo
// simulation and streams its events to any browser that connects.
package main

import (
	"context"
	"log"
	"net/http"
	"sort"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/simulation"
	"dispatchlab/internal/transport/ws"
)

const (
	addr        = ":8080"
	demoSeed    = 42
	driverCount = 12
	// demoOrderDelay gives a browser time to connect and receive the
	// startup snapshot before the demo order's events start flowing, so
	// the assignment sequence is visible instead of arriving mid-flight.
	demoOrderDelay = 2 * time.Second
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sim := simulation.New("demo", demoSeed, driverCount)
	go sim.Run(ctx)

	hub := ws.NewHub(sim.Events())

	// vertical slice: place one deterministic order after a short delay so
	// a connecting browser sees the full assignment sequence rather than
	// joining mid-flight. commanding orders from the browser lands in
	// phase 2's rest api.
	go func() {
		time.Sleep(demoOrderDelay)
		nodeIDs := make([]domain.NodeID, 0, len(sim.City.Nodes))
		for id := range sim.City.Nodes {
			nodeIDs = append(nodeIDs, id)
		}
		sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
		if len(nodeIDs) >= 2 {
			sim.Submit(simulation.PlaceOrder{
				Pickup:      nodeIDs[0],
				Destination: nodeIDs[len(nodeIDs)-1],
			})
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ws.Handler(hub))
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("dispatchlab server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
