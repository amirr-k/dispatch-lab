// Command server runs the DispatchLab backend: the REST command API and the
// WebSocket stream for any number of concurrent simulations.
package main

import (
	"log"
	"net/http"

	dispatchhttp "dispatchlab/internal/transport/http"

	"dispatchlab/internal/service"
)

const (
	addr = ":8080"
	// maxSimulations bounds guest-created simulations until phase 6 adds
	// session-scoped quotas; a flat cap is the simplest safe default.
	maxSimulations = 50
)

func main() {
	mgr := service.NewManager(maxSimulations)
	defer mgr.Shutdown()
	compare := service.NewComparisons()

	server := dispatchhttp.NewServer(mgr, compare)

	log.Printf("dispatchlab server listening on %s", addr)
	if err := http.ListenAndServe(addr, server.Routes()); err != nil {
		log.Fatal(err)
	}
}
