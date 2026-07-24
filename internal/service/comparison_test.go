package service

import (
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
	result := store.Create(42, 10)
	if result.ID == "" {
		t.Fatal("expected a generated id")
	}

	got, ok := store.Get(result.ID)
	if !ok {
		t.Fatal("expected the created comparison to be retrievable")
	}
	if !reflect.DeepEqual(got, result) {
		t.Fatalf("expected Get to return the stored result unchanged, got %+v vs %+v", got, result)
	}
}

func TestComparisonsGetMissing(t *testing.T) {
	store := NewComparisons()
	if _, ok := store.Get("does-not-exist"); ok {
		t.Fatal("expected missing comparison to report not found")
	}
}
