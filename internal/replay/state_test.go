package replay_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/replay"
	"dispatchlab/internal/simulation"
	"dispatchlab/internal/store"
)

// runScenario drives a simulation headlessly through orders, a road closure,
// a pause, and a speed change, and returns every event it produced along with
// the simulation itself.
func runScenario(t *testing.T) (*simulation.Simulation, []store.Event) {
	t.Helper()

	sim := simulation.New("sim-replay", 42, 6)
	var events []domain.Event
	events = append(events, sim.Start()...)

	nodes := sortedNodeIDs(sim)
	events = append(events, sim.Apply(simulation.PlaceOrder{Pickup: nodes[0], Destination: nodes[len(nodes)-1]})...)
	for i := 0; i < 3; i++ {
		events = append(events, sim.Advance()...)
	}

	events = append(events, sim.Apply(simulation.PlaceOrder{Pickup: nodes[2], Destination: nodes[7]})...)
	events = append(events, sim.Apply(simulation.SetSpeed{Multiplier: 4})...)

	// close a road some driver is actually using, so the log contains a
	// closure, an invalidation, and a recomputed route.
	if edge, ok := firstRoutedEdge(sim); ok {
		events = append(events, sim.Apply(simulation.CloseRoad{EdgeID: edge})...)
	}

	for i := 0; i < 25; i++ {
		events = append(events, sim.Advance()...)
	}
	events = append(events, sim.Apply(simulation.SetPaused{Paused: true})...)
	events = append(events, sim.Advance()...)

	records := make([]store.Event, 0, len(events))
	for _, e := range events {
		record, err := store.EventFrom(e, "", time.Now())
		if err != nil {
			t.Fatalf("EventFrom: %v", err)
		}
		records = append(records, record)
	}
	return sim, records
}

func sortedNodeIDs(sim *simulation.Simulation) []domain.NodeID {
	var state replay.State
	decodeInto(sim.Snapshot().Payload, &state)
	ids := make([]domain.NodeID, 0, len(state.Nodes))
	for _, n := range state.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// firstRoutedEdge finds an edge on some driver's remaining route.
func firstRoutedEdge(sim *simulation.Simulation) (domain.EdgeID, bool) {
	var state replay.State
	decodeInto(sim.Snapshot().Payload, &state)

	for _, d := range state.Drivers {
		if len(d.Route) < d.RouteIndex+2 {
			continue
		}
		from, to := d.Route[d.RouteIndex], d.Route[d.RouteIndex+1]
		for _, e := range state.Edges {
			if e.From == from && e.To == to {
				return e.ID, true
			}
		}
	}
	return "", false
}

func decodeInto(payload any, dst any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		panic(err)
	}
}

func snapshotState(t *testing.T, sim *simulation.Simulation) replay.State {
	t.Helper()
	event := sim.Snapshot()
	var state replay.State
	decodeInto(event.Payload, &state)
	state.SimulationID = event.SimulationID
	state.Sequence = event.Sequence
	state.VirtualTime = event.VirtualTime
	return state
}

// the reconstruction is only trustworthy if folding the log lands on exactly
// the state the simulation itself holds.
func TestReconstructedStateMatchesLiveSimulation(t *testing.T) {
	sim, events := runScenario(t)

	got, err := replay.StateAt("sim-replay", nil, events, 0)
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	want := snapshotState(t, sim)

	if !reflect.DeepEqual(got.Drivers, want.Drivers) {
		t.Errorf("drivers differ\n got: %+v\nwant: %+v", got.Drivers, want.Drivers)
	}
	if !reflect.DeepEqual(got.Orders, want.Orders) {
		t.Errorf("orders differ\n got: %+v\nwant: %+v", got.Orders, want.Orders)
	}
	if !reflect.DeepEqual(got.Edges, want.Edges) {
		t.Error("edges differ; a road closure was not reconstructed")
	}
	if !reflect.DeepEqual(got.Nodes, want.Nodes) {
		t.Error("nodes differ")
	}
	if got.Paused != want.Paused {
		t.Errorf("paused = %v, want %v", got.Paused, want.Paused)
	}
	if got.Speed != want.Speed {
		t.Errorf("speed = %v, want %v", got.Speed, want.Speed)
	}
	if got.VirtualTime != want.VirtualTime {
		t.Errorf("virtual time = %v, want %v", got.VirtualTime, want.VirtualTime)
	}
	if got.Sequence != want.Sequence {
		t.Errorf("sequence = %d, want %d", got.Sequence, want.Sequence)
	}
}

// this is what makes periodic snapshots worth writing: starting from one has
// to give the same answer as folding the whole log.
func TestSnapshotStartMatchesFullFold(t *testing.T) {
	_, events := runScenario(t)
	target := events[len(events)-1].Sequence

	full, err := replay.StateAt("sim-replay", nil, events, target)
	if err != nil {
		t.Fatalf("full fold: %v", err)
	}

	// take a snapshot midway by reconstructing there, then restart from it.
	mid := events[len(events)/2].Sequence
	midState, err := replay.StateAt("sim-replay", nil, events, mid)
	if err != nil {
		t.Fatalf("mid fold: %v", err)
	}
	payload, err := json.Marshal(midState)
	if err != nil {
		t.Fatalf("marshal mid state: %v", err)
	}
	base := &store.Snapshot{
		SimulationID: "sim-replay",
		Sequence:     midState.Sequence,
		VirtualTime:  midState.VirtualTime,
		Payload:      payload,
	}

	fromSnapshot, err := replay.StateAt("sim-replay", base, events, target)
	if err != nil {
		t.Fatalf("fold from snapshot: %v", err)
	}

	if !reflect.DeepEqual(fromSnapshot.Drivers, full.Drivers) {
		t.Errorf("drivers differ\n from snapshot: %+v\n full fold: %+v", fromSnapshot.Drivers, full.Drivers)
	}
	if !reflect.DeepEqual(fromSnapshot.Orders, full.Orders) {
		t.Errorf("orders differ\n from snapshot: %+v\n full fold: %+v", fromSnapshot.Orders, full.Orders)
	}
	if !reflect.DeepEqual(fromSnapshot.Edges, full.Edges) {
		t.Error("edges differ between a snapshot start and a full fold")
	}
	if fromSnapshot.Sequence != full.Sequence || fromSnapshot.VirtualTime != full.VirtualTime {
		t.Errorf("clock differs: %d/%v vs %d/%v",
			fromSnapshot.Sequence, fromSnapshot.VirtualTime, full.Sequence, full.VirtualTime)
	}
}

// scrubbing backwards must show an earlier world, not the final one.
func TestStateAtIsScrubbable(t *testing.T) {
	_, events := runScenario(t)
	last := events[len(events)-1].Sequence

	early, err := replay.StateAt("sim-replay", nil, events, 2)
	if err != nil {
		t.Fatalf("StateAt(2): %v", err)
	}
	late, err := replay.StateAt("sim-replay", nil, events, last)
	if err != nil {
		t.Fatalf("StateAt(last): %v", err)
	}

	if early.Sequence != 2 {
		t.Errorf("early sequence = %d, want 2", early.Sequence)
	}
	if early.VirtualTime > late.VirtualTime {
		t.Error("virtual time went backwards as the sequence went forwards")
	}
	if len(early.Orders) >= len(late.Orders) {
		t.Errorf("expected fewer orders early on: %d vs %d", len(early.Orders), len(late.Orders))
	}

	moved := false
	for i := range early.Drivers {
		if early.Drivers[i].Position != late.Drivers[i].Position {
			moved = true
			break
		}
	}
	if !moved {
		t.Error("no driver moved between the first and last frames")
	}

	// every intermediate point must reconstruct without error.
	for seq := 1; seq <= last; seq++ {
		if _, err := replay.StateAt("sim-replay", nil, events, seq); err != nil {
			t.Fatalf("StateAt(%d): %v", seq, err)
		}
	}
}

func TestStateAtWithoutEventsIsEmpty(t *testing.T) {
	state, err := replay.StateAt("sim-empty", nil, nil, 0)
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	if state.SimulationID != "sim-empty" || len(state.Drivers) != 0 || len(state.Nodes) != 0 {
		t.Errorf("expected an empty state, got %+v", state)
	}
	if state.Speed != 1 {
		t.Errorf("speed = %v, want the default 1", state.Speed)
	}
}

func TestStateAtRejectsMalformedPayload(t *testing.T) {
	events := []store.Event{{
		SimulationID: "sim-bad",
		Sequence:     1,
		Type:         domain.EventOrderPlaced,
		Payload:      json.RawMessage(`{"orderId": 42}`),
	}}
	if _, err := replay.StateAt("sim-bad", nil, events, 0); err == nil {
		t.Fatal("expected an error decoding a payload with the wrong field type")
	}
}

func TestReaderLoadsAndReconstructsFromStore(t *testing.T) {
	ctx := context.Background()
	sim, events := runScenario(t)

	s := store.NewMemory()
	if err := s.CreateSimulation(ctx, store.Simulation{ID: "sim-replay", Seed: 42, Drivers: 6, Strategy: "baseline"}); err != nil {
		t.Fatalf("CreateSimulation: %v", err)
	}
	if err := s.AppendEvents(ctx, events); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	reader := replay.NewReader(s)

	log, err := reader.Load(ctx, "sim-replay", 0, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(log.Events) != len(events) {
		t.Fatalf("loaded %d events, want %d", len(log.Events), len(events))
	}
	if log.Events[0].Type != domain.EventSimulationSnapshot {
		t.Errorf("first event is %q, want the opening snapshot", log.Events[0].Type)
	}
	if log.LatestSequence != events[len(events)-1].Sequence {
		t.Errorf("latest sequence = %d, want %d", log.LatestSequence, events[len(events)-1].Sequence)
	}
	if log.HasMore {
		t.Error("HasMore should be false when the whole log fits in one page")
	}
	if log.Simulation.Seed != 42 {
		t.Errorf("metadata not returned: %+v", log.Simulation)
	}

	page, err := reader.Load(ctx, "sim-replay", 0, 5)
	if err != nil {
		t.Fatalf("Load page: %v", err)
	}
	if len(page.Events) != 5 || !page.HasMore {
		t.Errorf("page has %d events, hasMore = %v; want 5 and true", len(page.Events), page.HasMore)
	}

	// with no snapshot stored, StateAt has to fold the log from the start.
	state, err := reader.StateAt(ctx, "sim-replay", 0)
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	want := snapshotState(t, sim)
	if !reflect.DeepEqual(state.Drivers, want.Drivers) {
		t.Errorf("drivers differ\n got: %+v\nwant: %+v", state.Drivers, want.Drivers)
	}

	// and with one stored, it has to agree with the version that folded
	// everything.
	midEvents := events[:len(events)/2]
	midState, err := replay.StateAt("sim-replay", nil, midEvents, 0)
	if err != nil {
		t.Fatalf("mid fold: %v", err)
	}
	payload, err := json.Marshal(midState)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := s.SaveSnapshot(ctx, store.Snapshot{
		SimulationID: "sim-replay",
		Sequence:     midState.Sequence,
		VirtualTime:  midState.VirtualTime,
		Payload:      payload,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	fromSnapshot, err := reader.StateAt(ctx, "sim-replay", 0)
	if err != nil {
		t.Fatalf("StateAt after snapshot: %v", err)
	}
	if !reflect.DeepEqual(fromSnapshot.Drivers, state.Drivers) {
		t.Errorf("snapshot-backed reconstruction differs\n got: %+v\nwant: %+v", fromSnapshot.Drivers, state.Drivers)
	}
}

func TestReaderReportsUnknownSimulation(t *testing.T) {
	reader := replay.NewReader(store.NewMemory())

	if _, err := reader.Load(context.Background(), "nope", 0, 0); err != replay.ErrNotFound {
		t.Errorf("Load error = %v, want ErrNotFound", err)
	}
	if _, err := reader.StateAt(context.Background(), "nope", 0); err != replay.ErrNotFound {
		t.Errorf("StateAt error = %v, want ErrNotFound", err)
	}
}
