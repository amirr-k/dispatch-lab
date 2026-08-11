package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/store"
)

func provision(t *testing.T, s store.Store) []ShowcaseRun {
	t.Helper()
	runs := DefaultShowcaseRuns()
	if err := ProvisionShowcases(context.Background(), s, runs, nil, nil); err != nil {
		t.Fatalf("ProvisionShowcases: %v", err)
	}
	return runs
}

func TestShowcaseRunsAreProvisioned(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	runs := provision(t, s)

	for _, run := range runs {
		sim, err := s.GetSimulation(ctx, run.ID)
		if err != nil {
			t.Fatalf("showcase %s was not created: %v", run.ID, err)
		}
		if !sim.Showcase {
			t.Errorf("%s is not marked as a showcase", run.ID)
		}
		if sim.ExpiresAt != nil {
			t.Errorf("%s has an expiry; showcase runs are never swept", run.ID)
		}
		if sim.GuestToken != "" {
			t.Errorf("%s has an owner; server-provisioned runs belong to nobody", run.ID)
		}

		events, err := s.Events(ctx, run.ID, 0, 10000)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		if len(events) < 10 {
			t.Errorf("%s has only %d events, which is not much of a demo", run.ID, len(events))
		}
		if events[0].Type != domain.EventSimulationSnapshot {
			t.Errorf("%s does not open with a snapshot", run.ID)
		}

		if _, err := s.SnapshotAtOrBefore(ctx, run.ID, 1<<30); err != nil {
			t.Errorf("%s has no final snapshot: %v", run.ID, err)
		}
	}
}

// withoutMeasuredLatency drops recalculationMs, the one wall-clock value the
// event stream still carries. The frontend shows it as "Routes recalculated
// in [measured duration]" — it is a real measurement of a real
// recomputation, so it varies by microseconds between runs while nothing
// else the visitor sees does.
func withoutMeasuredLatency(payload []byte) string {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return string(payload)
	}
	delete(decoded, "recalculationMs")

	normalized, err := json.Marshal(decoded)
	if err != nil {
		return string(payload)
	}
	return string(normalized)
}

// the whole point of a seeded showcase is that a fresh database regenerates
// the identical run, so the URL in a README keeps showing the same thing.
func TestShowcaseGenerationIsDeterministic(t *testing.T) {
	ctx := context.Background()

	first := store.NewMemory()
	second := store.NewMemory()
	runs := provision(t, first)
	provision(t, second)

	for _, run := range runs {
		a, err := first.Events(ctx, run.ID, 0, 10000)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		b, err := second.Events(ctx, run.ID, 0, 10000)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}

		if len(a) != len(b) {
			t.Fatalf("%s produced %d events one run and %d the next", run.ID, len(a), len(b))
		}
		for i := range a {
			if a[i].Sequence != b[i].Sequence || a[i].Type != b[i].Type || a[i].VirtualTime != b[i].VirtualTime {
				t.Fatalf("%s event %d differs between generations: %+v vs %+v", run.ID, i, a[i], b[i])
			}
			if withoutMeasuredLatency(a[i].Payload) != withoutMeasuredLatency(b[i].Payload) {
				t.Fatalf("%s event %d payload differs between generations:\n %s\n %s",
					run.ID, i, a[i].Payload, b[i].Payload)
			}
		}
	}
}

// provisioning runs on every start, so it must leave an existing run alone
// rather than regenerate or duplicate it.
func TestProvisioningIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()

	runs := provision(t, s)
	before, err := s.Events(ctx, runs[0].ID, 0, 10000)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	provision(t, s)
	after, err := s.Events(ctx, runs[0].ID, 0, 10000)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	if len(before) != len(after) {
		t.Errorf("re-provisioning changed the event count from %d to %d", len(before), len(after))
	}
}

// the closure run has to actually contain a closure and the reroute it
// causes, or it is not demonstrating anything.
func TestClosureShowcaseContainsAReroute(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	provision(t, s)

	events, err := s.Events(ctx, "showcase-road-closure", 0, 10000)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	saw := make(map[domain.EventType]bool)
	for _, e := range events {
		saw[e.Type] = true
	}
	for _, want := range []domain.EventType{
		domain.EventOrderPlaced,
		domain.EventOrderAssigned,
		domain.EventRoadClosed,
		domain.EventRouteInvalidated,
	} {
		if !saw[want] {
			t.Errorf("the closure showcase contains no %s event", want)
		}
	}
}

func TestProvisioningWithoutAStoreIsANoOp(t *testing.T) {
	if err := ProvisionShowcases(context.Background(), nil, DefaultShowcaseRuns(), nil, nil); err != nil {
		t.Fatalf("ProvisionShowcases with no store: %v", err)
	}
}

func TestRetentionSweepsExpiredRunsAndSessions(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	now := time.Now().UTC()

	if err := s.CreateGuestSession(ctx, store.GuestSession{
		Token: "dead", CreatedAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreateGuestSession: %v", err)
	}
	past := now.Add(-time.Minute)
	if err := s.CreateSimulation(ctx, store.Simulation{
		ID: "sim-old", Seed: 1, Drivers: 2, GuestToken: "dead", ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("CreateSimulation: %v", err)
	}

	// a provisioned showcase must survive the sweep.
	provision(t, s)

	retention := NewRetention(RetentionConfig{Store: s})
	result := retention.Sweep(ctx)

	if result.Simulations != 1 || result.Sessions != 1 {
		t.Fatalf("swept %+v, want 1 simulation and 1 session", result)
	}
	if _, err := s.GetSimulation(ctx, "sim-old"); err == nil {
		t.Error("the expired run survived the sweep")
	}
	if _, err := s.GetSimulation(ctx, "showcase-first-delivery"); err != nil {
		t.Errorf("a showcase run was swept: %v", err)
	}
}

// without a database, sessions live in the session service's own map and the
// sweeper has to collect them there instead.
func TestRetentionSweepsFallbackSessions(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessions(SessionsConfig{TTL: time.Nanosecond})

	if _, err := sessions.Issue(ctx); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(time.Millisecond)

	retention := NewRetention(RetentionConfig{Sessions: sessions})
	if result := retention.Sweep(ctx); result.Sessions != 1 {
		t.Errorf("swept %d fallback sessions, want 1", result.Sessions)
	}
}

func TestRetentionRunStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	retention := NewRetention(RetentionConfig{Store: store.NewMemory(), Interval: time.Millisecond})

	done := make(chan struct{})
	go func() {
		retention.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the sweeper did not stop when its context was cancelled")
	}
}
