// Package storetest holds the conformance suite every store.Store
// implementation must pass. It lives in its own package so both the in-memory
// store and the Postgres store can run the exact same tests.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/store"
)

// Factory builds a fresh, empty store for one test.
type Factory func(t *testing.T) store.Store

// Run executes the full conformance suite against a store implementation.
func Run(t *testing.T, newStore Factory) {
	t.Helper()

	tests := map[string]func(*testing.T, store.Store){
		"SimulationRoundTrip":         testSimulationRoundTrip,
		"MissingRowsReportNotFound":   testMissingRowsReportNotFound,
		"MarkShowcase":                testMarkShowcase,
		"EventsAreOrderedAndPaged":    testEventsAreOrderedAndPaged,
		"AppendIsIdempotent":          testAppendIsIdempotent,
		"LatestSequence":              testLatestSequence,
		"SnapshotAtOrBefore":          testSnapshotAtOrBefore,
		"SnapshotUpsert":              testSnapshotUpsert,
		"ComparisonRoundTrip":         testComparisonRoundTrip,
		"ConcurrentAppendsAreVisible": testConcurrentAppendsAreVisible,
	}

	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			fn(t, s)
		})
	}
}

func seedSimulation(t *testing.T, s store.Store, id string) store.Simulation {
	t.Helper()
	sim := store.Simulation{
		ID:        id,
		Seed:      42,
		Drivers:   12,
		Strategy:  "baseline",
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.CreateSimulation(context.Background(), sim); err != nil {
		t.Fatalf("CreateSimulation: %v", err)
	}
	return sim
}

func event(simID string, sequence int, typ domain.EventType) store.Event {
	payload, _ := json.Marshal(map[string]any{"seq": sequence})
	return store.Event{
		SimulationID: simID,
		Sequence:     sequence,
		VirtualTime:  float64(sequence) * 0.5,
		Type:         typ,
		Payload:      payload,
		TraceID:      "trace-" + string(typ),
		RecordedAt:   time.Now().UTC(),
	}
}

func testSimulationRoundTrip(t *testing.T, s store.Store) {
	ctx := context.Background()
	want := seedSimulation(t, s, "sim-round-trip")

	got, err := s.GetSimulation(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetSimulation: %v", err)
	}
	if got.ID != want.ID || got.Seed != want.Seed || got.Drivers != want.Drivers || got.Strategy != want.Strategy {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
	if got.Showcase {
		t.Error("a new simulation should not be marked showcase")
	}
	if got.CompletedAt != nil {
		t.Error("a new simulation should have no completion time")
	}

	// creating the same id twice must not error or clobber the original row.
	dup := want
	dup.Seed = 99
	if err := s.CreateSimulation(ctx, dup); err != nil {
		t.Fatalf("duplicate CreateSimulation: %v", err)
	}
	got, err = s.GetSimulation(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetSimulation after duplicate: %v", err)
	}
	if got.Seed != want.Seed {
		t.Errorf("duplicate create overwrote the row: seed = %d", got.Seed)
	}
}

func testMissingRowsReportNotFound(t *testing.T, s store.Store) {
	ctx := context.Background()

	if _, err := s.GetSimulation(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSimulation error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetComparison(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetComparison error = %v, want ErrNotFound", err)
	}
	if _, err := s.SnapshotAtOrBefore(ctx, "nope", 100); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SnapshotAtOrBefore error = %v, want ErrNotFound", err)
	}
	if err := s.MarkShowcase(ctx, "nope", time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("MarkShowcase error = %v, want ErrNotFound", err)
	}

	events, err := s.Events(ctx, "nope", 0, 10)
	if err != nil {
		t.Errorf("Events on an unknown simulation should not error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("Events on an unknown simulation returned %d rows", len(events))
	}
	if seq, err := s.LatestSequence(ctx, "nope"); err != nil || seq != 0 {
		t.Errorf("LatestSequence on an unknown simulation = %d, %v; want 0, nil", seq, err)
	}
}

func testMarkShowcase(t *testing.T, s store.Store) {
	ctx := context.Background()
	sim := seedSimulation(t, s, "sim-showcase")

	completed := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.MarkShowcase(ctx, sim.ID, completed); err != nil {
		t.Fatalf("MarkShowcase: %v", err)
	}

	got, err := s.GetSimulation(ctx, sim.ID)
	if err != nil {
		t.Fatalf("GetSimulation: %v", err)
	}
	if !got.Showcase {
		t.Error("showcase flag was not persisted")
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Errorf("completedAt = %v, want %v", got.CompletedAt, completed)
	}
}

func testEventsAreOrderedAndPaged(t *testing.T, s store.Store) {
	ctx := context.Background()
	sim := seedSimulation(t, s, "sim-events")

	// written out of order on purpose: reads must come back sequenced.
	batch := []store.Event{
		event(sim.ID, 3, domain.EventOrderAssigned),
		event(sim.ID, 1, domain.EventSimulationSnapshot),
		event(sim.ID, 2, domain.EventOrderPlaced),
		event(sim.ID, 4, domain.EventDriverPositionUpdate),
	}
	if err := s.AppendEvents(ctx, batch); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	all, err := s.Events(ctx, sim.ID, 0, 100)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d events, want 4", len(all))
	}
	for i, e := range all {
		if e.Sequence != i+1 {
			t.Fatalf("event %d has sequence %d, want %d", i, e.Sequence, i+1)
		}
	}

	if all[1].Type != domain.EventOrderPlaced {
		t.Errorf("event type not preserved: %v", all[1].Type)
	}
	if all[1].VirtualTime != 1.0 {
		t.Errorf("virtual time not preserved: %v", all[1].VirtualTime)
	}
	if !strings.HasPrefix(all[1].TraceID, "trace-") {
		t.Errorf("trace id not preserved: %q", all[1].TraceID)
	}
	var payload map[string]any
	if err := json.Unmarshal(all[1].Payload, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if payload["seq"] != float64(2) {
		t.Errorf("payload not preserved: %v", payload)
	}

	// fromSequence is exclusive, and limit bounds the page.
	page, err := s.Events(ctx, sim.ID, 2, 2)
	if err != nil {
		t.Fatalf("Events page: %v", err)
	}
	if len(page) != 2 || page[0].Sequence != 3 || page[1].Sequence != 4 {
		t.Fatalf("page = %+v, want sequences 3 and 4", page)
	}

	empty, err := s.Events(ctx, sim.ID, 4, 10)
	if err != nil {
		t.Fatalf("Events past the end: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected no events past the last sequence, got %d", len(empty))
	}
}

func testAppendIsIdempotent(t *testing.T, s store.Store) {
	ctx := context.Background()
	sim := seedSimulation(t, s, "sim-idempotent")

	batch := []store.Event{
		event(sim.ID, 1, domain.EventOrderPlaced),
		event(sim.ID, 2, domain.EventOrderAssigned),
	}
	if err := s.AppendEvents(ctx, batch); err != nil {
		t.Fatalf("first AppendEvents: %v", err)
	}
	// a retried flush overlaps the previous one; the log is keyed by
	// (simulation, sequence) so the overlap must not duplicate.
	retry := append(batch, event(sim.ID, 3, domain.EventOrderDelivered))
	if err := s.AppendEvents(ctx, retry); err != nil {
		t.Fatalf("retried AppendEvents: %v", err)
	}

	all, err := s.Events(ctx, sim.ID, 0, 100)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d events after a retried batch, want 3", len(all))
	}

	if err := s.AppendEvents(ctx, nil); err != nil {
		t.Errorf("appending nothing should be a no-op: %v", err)
	}
}

func testLatestSequence(t *testing.T, s store.Store) {
	ctx := context.Background()
	sim := seedSimulation(t, s, "sim-latest")

	if seq, err := s.LatestSequence(ctx, sim.ID); err != nil || seq != 0 {
		t.Fatalf("LatestSequence with no events = %d, %v; want 0, nil", seq, err)
	}

	if err := s.AppendEvents(ctx, []store.Event{
		event(sim.ID, 1, domain.EventOrderPlaced),
		event(sim.ID, 7, domain.EventOrderDelivered),
		event(sim.ID, 4, domain.EventOrderAssigned),
	}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	if seq, err := s.LatestSequence(ctx, sim.ID); err != nil || seq != 7 {
		t.Fatalf("LatestSequence = %d, %v; want 7, nil", seq, err)
	}
}

func snapshot(simID string, sequence int) store.Snapshot {
	payload, _ := json.Marshal(map[string]any{"atSequence": sequence})
	return store.Snapshot{
		SimulationID: simID,
		Sequence:     sequence,
		VirtualTime:  float64(sequence),
		Payload:      payload,
		RecordedAt:   time.Now().UTC(),
	}
}

func testSnapshotAtOrBefore(t *testing.T, s store.Store) {
	ctx := context.Background()
	sim := seedSimulation(t, s, "sim-snapshots")

	for _, seq := range []int{1, 50, 100} {
		if err := s.SaveSnapshot(ctx, snapshot(sim.ID, seq)); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
	}

	cases := []struct {
		target int
		want   int
	}{
		{target: 1, want: 1},
		{target: 49, want: 1},
		{target: 50, want: 50},
		{target: 99, want: 50},
		{target: 100, want: 100},
		{target: 5000, want: 100},
	}
	for _, tc := range cases {
		got, err := s.SnapshotAtOrBefore(ctx, sim.ID, tc.target)
		if err != nil {
			t.Fatalf("SnapshotAtOrBefore(%d): %v", tc.target, err)
		}
		if got.Sequence != tc.want {
			t.Errorf("SnapshotAtOrBefore(%d) = %d, want %d", tc.target, got.Sequence, tc.want)
		}
		if got.VirtualTime != float64(tc.want) {
			t.Errorf("SnapshotAtOrBefore(%d) virtual time = %v, want %v", tc.target, got.VirtualTime, float64(tc.want))
		}
	}

	// nothing at or before sequence 0: replay has to start from the beginning.
	if _, err := s.SnapshotAtOrBefore(ctx, sim.ID, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SnapshotAtOrBefore(0) error = %v, want ErrNotFound", err)
	}
}

func testSnapshotUpsert(t *testing.T, s store.Store) {
	ctx := context.Background()
	sim := seedSimulation(t, s, "sim-snapshot-upsert")

	first := snapshot(sim.ID, 10)
	if err := s.SaveSnapshot(ctx, first); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	second := first
	second.Payload = json.RawMessage(`{"atSequence":10,"rewritten":true}`)
	if err := s.SaveSnapshot(ctx, second); err != nil {
		t.Fatalf("SaveSnapshot rewrite: %v", err)
	}

	got, err := s.SnapshotAtOrBefore(ctx, sim.ID, 10)
	if err != nil {
		t.Fatalf("SnapshotAtOrBefore: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["rewritten"] != true {
		t.Errorf("rewriting a snapshot at the same sequence did not take: %v", payload)
	}
}

func testComparisonRoundTrip(t *testing.T, s store.Store) {
	ctx := context.Background()
	want := store.Comparison{
		ID:        "cmp-1",
		Seed:      42,
		Drivers:   12,
		Result:    json.RawMessage(`{"baseline":{"completedDeliveries":18}}`),
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.SaveComparison(ctx, want); err != nil {
		t.Fatalf("SaveComparison: %v", err)
	}

	got, err := s.GetComparison(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetComparison: %v", err)
	}
	if got.ID != want.ID || got.Seed != want.Seed || got.Drivers != want.Drivers {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	var result map[string]any
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("result payload: %v", err)
	}
	if _, ok := result["baseline"]; !ok {
		t.Errorf("result payload not preserved: %v", result)
	}
}

func testConcurrentAppendsAreVisible(t *testing.T, s store.Store) {
	ctx := context.Background()
	sim := seedSimulation(t, s, "sim-concurrent")

	const writers, perWriter = 4, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			batch := make([]store.Event, 0, perWriter)
			for i := 0; i < perWriter; i++ {
				batch = append(batch, event(sim.ID, w*perWriter+i+1, domain.EventDriverPositionUpdate))
			}
			if err := s.AppendEvents(ctx, batch); err != nil {
				errs <- err
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent AppendEvents: %v", err)
	}

	all, err := s.Events(ctx, sim.ID, 0, 1000)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(all) != writers*perWriter {
		t.Fatalf("got %d events, want %d", len(all), writers*perWriter)
	}
	for i, e := range all {
		if e.Sequence != i+1 {
			t.Fatalf("event %d has sequence %d, want %d", i, e.Sequence, i+1)
		}
	}
}
