package replay_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/replay"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

// blockingStore wraps a memory store so a test can hold up writes, fail them,
// or count how many batches were written.
type blockingStore struct {
	*store.Memory

	mu      sync.Mutex
	batches [][]store.Event

	release chan struct{}
	failErr error
}

func newBlockingStore() *blockingStore {
	return &blockingStore{Memory: store.NewMemory()}
}

func (b *blockingStore) AppendEvents(ctx context.Context, events []store.Event) error {
	if b.release != nil {
		<-b.release
	}

	b.mu.Lock()
	batch := make([]store.Event, len(events))
	copy(batch, events)
	b.batches = append(b.batches, batch)
	failErr := b.failErr
	b.mu.Unlock()

	if failErr != nil {
		return failErr
	}
	return b.Memory.AppendEvents(ctx, events)
}

func (b *blockingStore) batchCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.batches)
}

// stubSnapshotter answers snapshot requests, optionally slowly.
type stubSnapshotter struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	seq   int
}

func (s *stubSnapshotter) CurrentSnapshot() domain.Event {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.seq += 10
	return domain.Event{
		SchemaVersion: 1,
		SimulationID:  "sim-1",
		Sequence:      s.seq,
		VirtualTime:   float64(s.seq),
		Type:          domain.EventSimulationSnapshot,
		Payload:       map[string]any{"drivers": []any{}, "speed": 1.0},
	}
}

func (s *stubSnapshotter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func testEvent(sequence int) domain.Event {
	return domain.Event{
		SchemaVersion: 1,
		SimulationID:  "sim-1",
		Sequence:      sequence,
		VirtualTime:   float64(sequence),
		Type:          domain.EventDriverPositionUpdate,
		Payload:       map[string]any{"driverId": "driver-0", "nodeId": "node-1"},
		TraceID:       "trace-1",
	}
}

// drain reads every event from out until it closes.
func drain(out <-chan domain.Event) []domain.Event {
	var got []domain.Event
	for event := range out {
		got = append(got, event)
	}
	return got
}

func TestRecorderForwardsAndPersists(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	metrics := telemetry.NewMetrics()

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{
		Store:         s,
		Metrics:       metrics,
		BatchSize:     3,
		FlushInterval: 20 * time.Millisecond,
	})

	in := make(chan domain.Event, 16)
	out := recorder.Tap(ctx, in)
	for i := 1; i <= 7; i++ {
		in <- testEvent(i)
	}
	close(in)

	got := drain(out)
	if len(got) != 7 {
		t.Fatalf("forwarded %d events, want 7", len(got))
	}
	for i, event := range got {
		if event.Sequence != i+1 {
			t.Fatalf("event %d forwarded out of order: %+v", i, event)
		}
	}

	<-recorder.Done()

	stored, err := s.Events(ctx, "sim-1", 0, 100)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(stored) != 7 {
		t.Fatalf("persisted %d events, want 7", len(stored))
	}
	if stored[0].TraceID != "trace-1" {
		t.Errorf("trace id not persisted: %q", stored[0].TraceID)
	}
	var payload map[string]any
	if err := json.Unmarshal(stored[0].Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["driverId"] != "driver-0" {
		t.Errorf("payload not persisted: %v", payload)
	}
	if got := metrics.EventsPersisted().Value(); got != 7 {
		t.Errorf("events persisted metric = %v, want 7", got)
	}
}

// the persistence rules forbid a transaction per animation frame, so the
// recorder has to group events rather than write one at a time.
func TestRecorderWritesInBatches(t *testing.T) {
	ctx := context.Background()
	s := newBlockingStore()

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{
		Store:         s,
		BatchSize:     25,
		FlushInterval: time.Hour, // only the size trigger should fire here
	})

	in := make(chan domain.Event, 128)
	out := recorder.Tap(ctx, in)
	for i := 1; i <= 100; i++ {
		in <- testEvent(i)
	}
	close(in)
	drain(out)
	<-recorder.Done()

	if batches := s.batchCount(); batches > 5 {
		t.Errorf("wrote %d batches for 100 events, want at most 5", batches)
	}
	stored, err := s.Events(ctx, "sim-1", 0, 200)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(stored) != 100 {
		t.Errorf("persisted %d events, want 100", len(stored))
	}
}

// showcasing a run needs the store caught up right now, not whenever the
// next scheduled flush happens to land - this is the gap a real save-then-
// immediately-open-replay click surfaced (MarkShowcase wrote a final
// snapshot but never forced the event log itself to flush).
func TestFlushWritesBufferedEventsImmediately(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{
		Store: s,
		// long enough that neither trigger could plausibly fire on its own
		// during this test - only Flush should be moving anything.
		BatchSize:     1000,
		FlushInterval: time.Hour,
	})

	in := make(chan domain.Event, 16)
	out := recorder.Tap(ctx, in)
	for i := 1; i <= 5; i++ {
		in <- testEvent(i)
	}
	// draining forwarded copies guarantees forwardAndQueue has at least
	// attempted to enqueue every one of these five before Flush is called -
	// without it, this test would carry the same race it is proving Flush
	// closes for a real caller.
	for i := 0; i < 5; i++ {
		<-out
	}

	if stored, _ := s.Events(ctx, "sim-1", 0, 100); len(stored) != 0 {
		t.Fatalf("expected nothing persisted yet, got %d events", len(stored))
	}

	if err := recorder.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	stored, err := s.Events(ctx, "sim-1", 0, 100)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(stored) != 5 {
		t.Fatalf("persisted %d events after Flush, want 5", len(stored))
	}

	close(in)
	drain(out)
	<-recorder.Done()
}

// a second Flush after the writer has already exited must return promptly
// rather than block forever waiting for a writeLoop that is no longer there
// to answer flushRequests.
func TestFlushAfterRecorderHasStoppedReturnsImmediately(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{Store: s})

	in := make(chan domain.Event, 4)
	out := recorder.Tap(ctx, in)
	in <- testEvent(1)
	close(in)
	drain(out)
	<-recorder.Done()

	done := make(chan error, 1)
	go func() { done <- recorder.Flush(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Flush after stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Flush blocked after the writer had already exited")
	}
}

func TestRecorderWritesPeriodicSnapshots(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	metrics := telemetry.NewMetrics()
	snapshotter := &stubSnapshotter{}

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{
		Store:         s,
		Snapshotter:   snapshotter,
		Metrics:       metrics,
		BatchSize:     10,
		SnapshotEvery: 20,
		FlushInterval: 10 * time.Millisecond,
	})

	in := make(chan domain.Event, 128)
	out := recorder.Tap(ctx, in)

	drained := make(chan struct{})
	go func() {
		drain(out)
		close(drained)
	}()

	// snapshot requests coalesce onto a one-slot signal, so a single burst of
	// 60 events may legitimately raise only two. feeding one window at a time
	// and waiting for each snapshot is what makes the count deterministic.
	sequence := 0
	for window := 1; window <= 3; window++ {
		for i := 0; i < 20; i++ {
			sequence++
			in <- testEvent(sequence)
		}
		waitFor(t, time.Second, func() bool { return snapshotter.callCount() >= window })
	}

	close(in)
	<-drained
	<-recorder.Done()

	waitFor(t, time.Second, func() bool { return metrics.SnapshotsWritten().Value() >= 3 })

	snapshot, err := s.SnapshotAtOrBefore(ctx, "sim-1", 1000)
	if err != nil {
		t.Fatalf("SnapshotAtOrBefore: %v", err)
	}
	if snapshot.Sequence == 0 {
		t.Error("snapshot was stored without a sequence")
	}
}

// asking the simulation for a snapshot blocks on its actor loop, and that
// loop can be blocked feeding the recorder. If the recorder waited for
// snapshots inline, that would deadlock.
func TestRecorderKeepsForwardingWhileSnapshotting(t *testing.T) {
	ctx := context.Background()
	snapshotter := &stubSnapshotter{delay: 200 * time.Millisecond}

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{
		Store:         store.NewMemory(),
		Snapshotter:   snapshotter,
		BatchSize:     5,
		SnapshotEvery: 5,
		FlushInterval: 5 * time.Millisecond,
	})

	in := make(chan domain.Event, 4)
	out := recorder.Tap(ctx, in)

	done := make(chan []domain.Event, 1)
	go func() { done <- drain(out) }()

	for i := 1; i <= 50; i++ {
		in <- testEvent(i)
	}
	close(in)

	select {
	case got := <-done:
		if len(got) != 50 {
			t.Fatalf("forwarded %d events, want 50", len(got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forwarding stalled behind a slow snapshot")
	}
}

// a failing database must not take the live stream down with it.
func TestRecorderCountsPersistenceErrors(t *testing.T) {
	ctx := context.Background()
	s := newBlockingStore()
	s.failErr = errors.New("connection refused")
	metrics := telemetry.NewMetrics()

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{
		Store:         s,
		Metrics:       metrics,
		BatchSize:     5,
		FlushInterval: 10 * time.Millisecond,
	})

	in := make(chan domain.Event, 32)
	out := recorder.Tap(ctx, in)
	for i := 1; i <= 20; i++ {
		in <- testEvent(i)
	}
	close(in)

	if got := drain(out); len(got) != 20 {
		t.Fatalf("forwarded %d events despite store failures, want 20", len(got))
	}
	<-recorder.Done()

	if got := metrics.PersistenceErrors().Value(); got == 0 {
		t.Error("a failing store did not increment the persistence error counter")
	}
	if got := metrics.EventsPersisted().Value(); got != 0 {
		t.Errorf("events persisted = %v after every write failed, want 0", got)
	}
}

// when the database cannot keep up, events are dropped at the recorder rather
// than allowed to stall the simulation.
func TestRecorderDropsRatherThanBlocks(t *testing.T) {
	ctx := context.Background()
	s := newBlockingStore()
	s.release = make(chan struct{})
	metrics := telemetry.NewMetrics()

	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{
		Store:         s,
		Metrics:       metrics,
		BatchSize:     1,
		BufferSize:    4,
		FlushInterval: time.Millisecond,
	})

	in := make(chan domain.Event, 256)
	out := recorder.Tap(ctx, in)

	done := make(chan []domain.Event, 1)
	go func() { done <- drain(out) }()

	for i := 1; i <= 200; i++ {
		in <- testEvent(i)
	}
	close(in)

	select {
	case got := <-done:
		if len(got) != 200 {
			t.Fatalf("forwarded %d events while the store was stuck, want 200", len(got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forwarding blocked behind a stuck store")
	}

	if got := metrics.DroppedUpdates().Value(); got == 0 {
		t.Error("a stuck store did not increment the dropped-update counter")
	}
	close(s.release)
}

func TestRecorderWithoutStoreIsPassthrough(t *testing.T) {
	recorder := replay.NewRecorder("sim-1", replay.RecorderConfig{})

	in := make(chan domain.Event, 4)
	out := recorder.Tap(context.Background(), in)
	in <- testEvent(1)
	in <- testEvent(2)
	close(in)

	if got := drain(out); len(got) != 2 {
		t.Fatalf("forwarded %d events, want 2", len(got))
	}

	// a recorder with nothing to persist still has to report that it is
	// finished, or shutdown waits out its whole flush timeout on it.
	select {
	case <-recorder.Done():
	case <-time.After(time.Second):
		t.Fatal("a store-less recorder never signalled Done")
	}
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a condition")
}
