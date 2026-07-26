package service

import (
	"context"
	"reflect"
	"testing"
)

func TestDefaultScenarioDeterministic(t *testing.T) {
	a := DefaultScenario(42, 10)
	b := DefaultScenario(42, 10)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("expected the same seed and driver count to produce an identical scenario")
	}
}

func TestDefaultScenarioDiffersBySeed(t *testing.T) {
	a := DefaultScenario(1, 10)
	b := DefaultScenario(2, 10)
	if reflect.DeepEqual(a.Arrivals, b.Arrivals) {
		t.Fatal("expected different seeds to produce different demand")
	}
}

func TestDefaultScenarioAppliesDriverDefault(t *testing.T) {
	s := DefaultScenario(1, 0)
	if s.Drivers <= 0 {
		t.Fatalf("expected a positive default driver count, got %d", s.Drivers)
	}
}

// The three demand levels have to be genuinely different workloads, not just
// different labels: the whole reason the control exists is that contention
// between orders is what decides whether batch optimization can beat greedy
// nearest-driver at all.
func TestDemandLevelsProduceDifferentWorkloads(t *testing.T) {
	light := ScenarioFor(42, 12, DemandLight)
	steady := ScenarioFor(42, 12, DemandSteady)
	rush := ScenarioFor(42, 12, DemandRush)

	if !(len(light.Arrivals) < len(steady.Arrivals) && len(steady.Arrivals) < len(rush.Arrivals)) {
		t.Fatalf("expected order count to rise with demand, got %d/%d/%d",
			len(light.Arrivals), len(steady.Arrivals), len(rush.Arrivals))
	}

	// arrival rate matters more than raw count - the same orders spread thin
	// enough never compete for a driver.
	rate := func(s Scenario) float64 {
		last := s.Arrivals[len(s.Arrivals)-1].VirtualTime
		return float64(len(s.Arrivals)) / last
	}
	if !(rate(light) < rate(steady) && rate(steady) < rate(rush)) {
		t.Fatalf("expected arrival rate to rise with demand, got %.3f/%.3f/%.3f",
			rate(light), rate(steady), rate(rush))
	}

	for _, s := range []Scenario{light, steady, rush} {
		last := s.Arrivals[len(s.Arrivals)-1].VirtualTime
		if s.MaxVirtualTime <= last {
			t.Fatalf("demand %q cuts the run off at %.0f, before its last order even arrives at %.0f",
				s.Demand, s.MaxVirtualTime, last)
		}
	}
}

// The trade-off the comparison page exists to show, pinned to two concrete
// scenarios. Both are deterministic, so this cannot flake - if either
// direction ever reverses, the claim the page makes has stopped being true
// and the copy needs to change with it.
func TestDemandDecidesWhichStrategyWins(t *testing.T) {
	// spare capacity: every order gets a good driver either way, so the
	// optimizer's batch window is pure added delay and greedy wins.
	light := RunComparison(ScenarioFor(42, 12, DemandLight))
	if light.Optimized.AveragePickupTime <= light.Baseline.AveragePickupTime {
		t.Fatalf("expected batching to cost the optimizer time at light demand, got baseline %.2f vs optimized %.2f",
			light.Baseline.AveragePickupTime, light.Optimized.AveragePickupTime)
	}

	// real contention: orders outnumber free drivers, so choosing pairings
	// jointly beats claiming the nearest driver first-come-first-served.
	rush := RunComparison(ScenarioFor(42, 8, DemandRush))
	if rush.Optimized.AveragePickupTime >= rush.Baseline.AveragePickupTime {
		t.Fatalf("expected joint assignment to win under contention, got baseline %.2f vs optimized %.2f",
			rush.Baseline.AveragePickupTime, rush.Optimized.AveragePickupTime)
	}
	if rush.Optimized.UnassignedOrders > rush.Baseline.UnassignedOrders {
		t.Fatalf("expected the optimizer to leave no more orders unserved than baseline, got %d vs %d",
			rush.Optimized.UnassignedOrders, rush.Baseline.UnassignedOrders)
	}
}

func TestScenarioForDeterministicPerDemandLevel(t *testing.T) {
	for _, level := range []DemandLevel{DemandLight, DemandSteady, DemandRush} {
		if !reflect.DeepEqual(ScenarioFor(42, 10, level), ScenarioFor(42, 10, level)) {
			t.Fatalf("expected demand %q to reproduce an identical scenario", level)
		}
	}
}

func TestNormalizeDemandFallsBackToSteady(t *testing.T) {
	for _, raw := range []string{"", "LIGHT", "extreme", "0"} {
		if got := NormalizeDemand(raw); got != DemandSteady {
			t.Fatalf("expected %q to normalize to steady, got %q", raw, got)
		}
	}
	for _, level := range []DemandLevel{DemandLight, DemandSteady, DemandRush} {
		if got := NormalizeDemand(string(level)); got != level {
			t.Fatalf("expected %q to normalize to itself, got %q", level, got)
		}
	}
}

// DefaultScenario is what the showcase runs and every pre-demand stored
// comparison were generated from, so it has to keep producing exactly the
// steady workload it always did.
func TestDefaultScenarioIsSteadyDemand(t *testing.T) {
	if !reflect.DeepEqual(DefaultScenario(42, 10), ScenarioFor(42, 10, DemandSteady)) {
		t.Fatal("expected DefaultScenario to stay identical to steady demand")
	}
}

func TestRunComparisonDeterministic(t *testing.T) {
	scenario := DefaultScenario(42, 8)
	a := RunComparison(scenario)
	b := RunComparison(scenario)

	// compute-time fields are measured wall-clock latencies, not
	// simulation state, so they're excluded from the determinism check -
	// same rationale as the simulation package's own determinism test.
	a.Baseline.AssignmentComputeMs, b.Baseline.AssignmentComputeMs = 0, 0
	a.Optimized.AssignmentComputeMs, b.Optimized.AssignmentComputeMs = 0, 0

	if !reflect.DeepEqual(a.Baseline, b.Baseline) {
		t.Fatalf("expected deterministic baseline metrics, got %+v vs %+v", a.Baseline, b.Baseline)
	}
	if !reflect.DeepEqual(a.Optimized, b.Optimized) {
		t.Fatalf("expected deterministic optimized metrics, got %+v vs %+v", a.Optimized, b.Optimized)
	}
}

func TestRunComparisonBothStrategiesServeOrders(t *testing.T) {
	result := RunComparison(DefaultScenario(42, 10))

	if result.Baseline.CompletedDeliveries == 0 {
		t.Fatal("expected baseline to complete at least some deliveries over 20 orders and 200 virtual-time units")
	}
	if result.Optimized.CompletedDeliveries == 0 {
		t.Fatal("expected optimized to complete at least some deliveries")
	}
	if result.Baseline.TotalDistance <= 0 {
		t.Fatal("expected baseline to report a positive total distance")
	}
	if result.Optimized.TotalDistance <= 0 {
		t.Fatal("expected optimized to report a positive total distance")
	}
	if result.Baseline.AssignmentComputeMs < 0 || result.Optimized.AssignmentComputeMs < 0 {
		t.Fatal("expected non-negative compute time")
	}
}

// TestRunComparisonStrategiesActuallyDiffer is a sanity check that the two
// runs are genuinely independent: on a busy scenario (more demand than
// drivers), baseline's greedy nearest-first choices and optimized's joint
// batch solve should not land on identical metrics, which would suggest one
// strategy was wired to the other by mistake.
func TestRunComparisonStrategiesActuallyDiffer(t *testing.T) {
	result := RunComparison(DefaultScenario(42, 4))
	if reflect.DeepEqual(result.Baseline, result.Optimized) {
		t.Fatalf("expected baseline and optimized to produce different metrics on a busy scenario, both got %+v", result.Baseline)
	}
}

func TestRunComparisonAlgorithmLabels(t *testing.T) {
	result := RunComparison(DefaultScenario(1, 6))
	if result.Baseline.Algorithm != "baseline" {
		t.Fatalf("expected baseline label, got %q", result.Baseline.Algorithm)
	}
	if result.Optimized.Algorithm != "optimized" {
		t.Fatalf("expected optimized label, got %q", result.Optimized.Algorithm)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(sorted, 0.95); got != 10 {
		t.Fatalf("expected p95 of 1..10 to be 10, got %v", got)
	}
	if got := percentile(nil, 0.95); got != 0 {
		t.Fatalf("expected p95 of an empty slice to be 0, got %v", got)
	}
	if got := percentile([]float64{5}, 0.95); got != 5 {
		t.Fatalf("expected p95 of a single value to be that value, got %v", got)
	}
}

func TestComparisonsCreateAndGet(t *testing.T) {
	store := NewComparisons()
	result := store.Create(context.Background(), 42, 10, DemandSteady)
	if result.ID == "" {
		t.Fatal("expected a generated id")
	}

	got, ok := store.Get(context.Background(), result.ID)
	if !ok {
		t.Fatal("expected the created comparison to be retrievable")
	}
	if !reflect.DeepEqual(got, result) {
		t.Fatalf("expected Get to return the stored result unchanged, got %+v vs %+v", got, result)
	}
}

func TestComparisonsGetMissing(t *testing.T) {
	store := NewComparisons()
	if _, ok := store.Get(context.Background(), "does-not-exist"); ok {
		t.Fatal("expected missing comparison to report not found")
	}
}
