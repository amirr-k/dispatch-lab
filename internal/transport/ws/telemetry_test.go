package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/simulation"
	"dispatchlab/internal/telemetry"
)

// syncBuffer is a bytes.Buffer safe to read while the handler writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startInstrumentedServer serves the telemetry-aware handler over a real
// connection, since the publication path only runs against one.
func startInstrumentedServer(t *testing.T, sim *simulation.Simulation, hub *Hub, metrics *telemetry.Metrics, logs *syncBuffer) string {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sim.Run(ctx)

	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := http.NewServeMux()
	m.HandleFunc("/ws/{id}", HandlerWithTelemetry(testLookup(sim, hub), metrics, logger))

	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/" + sim.ID
}

// an event that arrived carrying a trace gets a publication span on that same
// trace, which is the last leg of the path from an http command to a browser.
func TestPublicationSpanContinuesTheEventsTrace(t *testing.T) {
	sim := simulation.New("sim-ws-trace", 1, 2)
	source := make(chan domain.Event, 8)
	hub := NewHub(source)
	logs := &syncBuffer{}

	conn := dial(t, startInstrumentedServer(t, sim, hub, telemetry.NewMetrics(), logs))

	// the snapshot arrives first; read it so the next read is the event under test.
	var snapshot domain.Event
	if err := conn.ReadJSON(&snapshot); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	source <- domain.Event{
		SchemaVersion: 1,
		SimulationID:  sim.ID,
		Sequence:      snapshot.Sequence + 1,
		Type:          domain.EventOrderPlaced,
		Payload:       map[string]any{"orderId": "order-1"},
		TraceID:       "trace-under-test",
	}

	var received domain.Event
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&received); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if received.TraceID != "trace-under-test" {
		t.Errorf("event reached the client without its trace: %q", received.TraceID)
	}

	waitForLog(t, logs, func(rec map[string]any) bool {
		return rec["span"] == "ws.publish" && rec["trace_id"] == "trace-under-test"
	})
}

// events the simulation clock produced belong to no request, so publishing
// them must not invent a trace.
func TestUntracedEventsProduceNoPublicationSpan(t *testing.T) {
	sim := simulation.New("sim-ws-untraced", 1, 2)
	source := make(chan domain.Event, 8)
	hub := NewHub(source)
	logs := &syncBuffer{}

	conn := dial(t, startInstrumentedServer(t, sim, hub, telemetry.NewMetrics(), logs))

	var snapshot domain.Event
	if err := conn.ReadJSON(&snapshot); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	source <- domain.Event{
		SchemaVersion: 1,
		SimulationID:  sim.ID,
		Sequence:      snapshot.Sequence + 1,
		Type:          domain.EventDriverPositionUpdate,
	}

	var received domain.Event
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.ReadJSON(&received); err != nil {
		t.Fatalf("read event: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if strings.Contains(logs.String(), "ws.publish") {
		t.Errorf("an untraced event produced a publication span:\n%s", logs.String())
	}
}

func TestConnectedClientsAreCounted(t *testing.T) {
	sim := simulation.New("sim-ws-count", 1, 2)
	source := make(chan domain.Event, 8)
	hub := NewHub(source)
	metrics := telemetry.NewMetrics()

	url := startInstrumentedServer(t, sim, hub, metrics, &syncBuffer{})

	first := dial(t, url)
	waitForValue(t, func() float64 { return metrics.WebSocketClients().Value() }, 1)

	second := dial(t, url)
	waitForValue(t, func() float64 { return metrics.WebSocketClients().Value() }, 2)

	second.Close()
	waitForValue(t, func() float64 { return metrics.WebSocketClients().Value() }, 1)

	first.Close()
	waitForValue(t, func() float64 { return metrics.WebSocketClients().Value() }, 0)
}

// a subscriber that never reads fills its buffer, and the hub counts what it
// had to throw away rather than blocking every other viewer.
func TestDroppedEventsAreCounted(t *testing.T) {
	source := make(chan domain.Event, 8)
	metrics := telemetry.NewMetrics()
	hub := NewHubWithMetrics(source, metrics)

	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	for i := 0; i < cap(sub)+50; i++ {
		source <- domain.Event{Sequence: i + 1, Type: domain.EventDriverPositionUpdate}
	}

	waitForValue(t, func() float64 { return metrics.DroppedUpdates().Value() }, 50)
}

func waitForValue(t *testing.T, read func() float64, want float64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if read() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("value settled at %v, want %v", read(), want)
}

func waitForLog(t *testing.T, logs *syncBuffer, match func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(logs.String(), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				continue
			}
			if match(rec) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log record matched:\n%s", logs.String())
}
