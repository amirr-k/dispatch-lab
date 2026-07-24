package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/simulation"

	"github.com/gorilla/websocket"
)

// testLookup wraps one live simulation behind the same Lookup signature the
// real service.Manager provides, without importing it (would create an
// import cycle: service already imports ws).
func testLookup(sim *simulation.Simulation, hub *Hub) Lookup {
	return func(id string) (*Hub, Snapshotter, bool) {
		if id != sim.ID {
			return nil, nil, false
		}
		return hub, sim, true
	}
}

// mux mirrors how the real http.Server registers this handler: PathValue
// only resolves "{id}" when routed through a pattern-aware ServeMux.
func mux(lookup Lookup) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/ws/{id}", Handler(lookup))
	return m
}

func startTestServer(t *testing.T, sim *simulation.Simulation, hub *Hub) string {
	t.Helper()
	srv := httptest.NewServer(mux(testLookup(sim, hub)))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/" + sim.ID
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestHandlerUnknownSimulationReturns404(t *testing.T) {
	sim := simulation.New("known", 1, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sim.Run(ctx)
	hub := NewHub(sim.Events())

	srv := httptest.NewServer(mux(testLookup(sim, hub)))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/unknown"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("expected dial to an unknown simulation id to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %+v", resp)
	}
}

func TestHandlerSendsSnapshotFirst(t *testing.T) {
	sim := simulation.New("s1", 1, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sim.Run(ctx)
	hub := NewHub(sim.Events())

	conn := dial(t, startTestServer(t, sim, hub))

	var first domain.Event
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read first message: %v", err)
	}
	if first.Type != domain.EventSimulationSnapshot {
		t.Fatalf("expected the first message to be a snapshot, got %s", first.Type)
	}
}

func TestReconnectSkipsAlreadySeenEvents(t *testing.T) {
	sim := simulation.New("s2", 1, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sim.Run(ctx)
	hub := NewHub(sim.Events())
	url := startTestServer(t, sim, hub)

	// drive some real events before the client ever connects, so the
	// snapshot it eventually gets already carries a nonzero sequence.
	nodeIDs := driverAccessibleNodes(sim)
	sim.Submit(simulation.PlaceOrder{Pickup: nodeIDs[0], Destination: nodeIDs[1]})
	time.Sleep(150 * time.Millisecond)

	conn := dial(t, url)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var snap domain.Event
	if err := conn.ReadJSON(&snap); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snap.Sequence == 0 {
		t.Fatal("expected the late-joining client's snapshot to reflect prior activity (nonzero sequence)")
	}

	// any further message must be strictly newer than the snapshot it
	// already has — no replay of events already folded into that snapshot.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for {
		var next domain.Event
		err := conn.ReadJSON(&next)
		if err != nil {
			return // no more messages within the deadline: nothing stale was resent
		}
		if next.Sequence <= snap.Sequence && next.Type != domain.EventSimulationSnapshot {
			t.Fatalf("received stale event (seq %d <= snapshot seq %d): %s", next.Sequence, snap.Sequence, next.Type)
		}
	}
}

func driverAccessibleNodes(sim *simulation.Simulation) []domain.NodeID {
	ids := make([]domain.NodeID, 0, len(sim.City.Nodes))
	for id := range sim.City.Nodes {
		ids = append(ids, id)
	}
	return ids
}
