package simulation

import (
	"context"
	"sort"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/telemetry"
)

func nodeIDs(s *Simulation) []domain.NodeID {
	ids := make([]domain.NodeID, 0, len(s.City.Nodes))
	for id := range s.City.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// a command's trace has to reach the events it produces, which is what lets
// one request be followed from the http handler to the browser.
func TestEventsCarryTheCommandsTrace(t *testing.T) {
	metrics := telemetry.NewMetrics()
	sim := NewWithConfig(Config{ID: "sim-trace", Seed: 7, DriverCount: 4, Metrics: metrics})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sim.Run(ctx)

	traced, span := telemetry.StartSpan(context.Background(), "http.place_order")
	defer span.End()

	ids := nodeIDs(sim)
	sim.SubmitCtx(traced, PlaceOrder{Pickup: ids[0], Destination: ids[len(ids)-1]})

	deadline := time.After(2 * time.Second)
	var assigned domain.Event
	for assigned.Type == "" {
		select {
		case event := <-sim.Events():
			if event.Type == domain.EventOrderAssigned {
				assigned = event
			}
		case <-deadline:
			t.Fatal("timed out waiting for an assignment")
		}
	}

	if assigned.TraceID == "" {
		t.Fatal("the assignment event carries no trace id")
	}
	if assigned.TraceID != span.TraceID() {
		t.Errorf("event trace id = %q, want the command's %q", assigned.TraceID, span.TraceID())
	}
}

// events the simulation produces on its own clock belong to no request, so
// they must not be stamped with the trace of whichever command ran last.
func TestClockEventsCarryNoTrace(t *testing.T) {
	sim := NewWithConfig(Config{ID: "sim-clock", Seed: 7, DriverCount: 4})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sim.Run(ctx)

	traced, span := telemetry.StartSpan(context.Background(), "http.place_order")
	defer span.End()

	ids := nodeIDs(sim)
	sim.SubmitCtx(traced, PlaceOrder{Pickup: ids[0], Destination: ids[len(ids)-1]})

	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-sim.Events():
			if event.Type != domain.EventDriverPositionUpdate {
				continue
			}
			if event.TraceID != "" {
				t.Fatalf("a tick-produced event carries trace id %q", event.TraceID)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for a position update")
		}
	}
}

func TestUntracedCommandsStillWork(t *testing.T) {
	sim := New("sim-untraced", 7, 4)
	sim.Start()

	ids := nodeIDs(sim)
	events := sim.Apply(PlaceOrder{Pickup: ids[0], Destination: ids[len(ids)-1]})
	if len(events) == 0 {
		t.Fatal("a command submitted without a trace produced no events")
	}
	for _, event := range events {
		if event.TraceID != "" {
			t.Errorf("untraced command produced an event with trace id %q", event.TraceID)
		}
	}
}

func TestRouteAndMatchLatencyAreRecorded(t *testing.T) {
	metrics := telemetry.NewMetrics()
	sim := NewWithConfig(Config{ID: "sim-metrics", Seed: 7, DriverCount: 4, Metrics: metrics})
	sim.Start()

	ids := nodeIDs(sim)
	sim.Apply(PlaceOrder{Pickup: ids[0], Destination: ids[len(ids)-1]})

	if got := metrics.MatchLatency().Count(); got == 0 {
		t.Error("no match latency was recorded for an assignment")
	}
	if got := metrics.RouteLatency().Count(); got == 0 {
		t.Error("no route latency was recorded for an assignment")
	}
}

// a nil metrics set is the default for headless callers, and must not panic.
func TestNilMetricsIsSafe(t *testing.T) {
	sim := NewWithConfig(Config{ID: "sim-nil-metrics", Seed: 7, DriverCount: 4})
	sim.Start()

	ids := nodeIDs(sim)
	sim.Apply(PlaceOrder{Pickup: ids[0], Destination: ids[len(ids)-1]})
	sim.Advance()
}
