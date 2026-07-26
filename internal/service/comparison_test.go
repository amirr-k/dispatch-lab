package service

import (
	"context"
	"fmt"
	"os"
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
	// enough never compete for a driver. Rush is a burst, so its rate is
	// effectively infinite; light and steady are spread out and can be ranked.
	last := func(s Scenario) float64 {
		return s.Arrivals[len(s.Arrivals)-1].VirtualTime
	}
	if last(steady) <= 0 {
		t.Fatalf("expected steady demand to have a positive arrival window, got %.1f", last(steady))
	}
	if last(rush) != 0 {
		t.Fatalf("expected rush demand to be a single burst at time 0, last arrival at %.1f", last(rush))
	}

	rate := func(s Scenario) float64 {
		return float64(len(s.Arrivals)) / last(s)
	}
	if !(rate(light) < rate(steady)) {
		t.Fatalf("expected light demand to have a lower arrival rate than steady, got %.3f vs %.3f",
			rate(light), rate(steady))
	}

	for _, s := range []Scenario{light, steady} {
		if s.MaxVirtualTime <= last(s) {
			t.Fatalf("demand %q cuts the run off at %.0f, before its last order even arrives at %.0f",
				s.Demand, s.MaxVirtualTime, last(s))
		}
	}
}

// The comparison page used to assume light demand made optimized lose on
// pickup time. Fair metrics plus adaptive max-wait change that story: the
// gate is "no regression," not "light must lose." Under rush, optimized must
// not regress on pickup, distance, completions, or unassigned; every cell
// must at least match baseline on completions / unassigned / served fraction.
// Sparse light/steady cells may still pay up to MaxWaitVirtualTime on pickup
// when orders never fill a batch — that structural wait is not treated as an
// algorithm regression (see plan risk note / resume wording).
func TestDemandDecidesWhichStrategyWins(t *testing.T) {
	assertNoServiceRegression(t, RunComparison(ScenarioFor(42, 12, DemandLight)))
	assertNoServiceRegression(t, RunComparison(ScenarioFor(42, 8, DemandRush)))
	assertNoRushMetricRegression(t, RunComparison(ScenarioFor(42, 8, DemandRush)))
}

func TestCanonicalComparisonSuite(t *testing.T) {
	seeds := []int64{42, 7, 99}
	demands := []DemandLevel{DemandLight, DemandSteady, DemandRush}
	drivers := []int{4, 12}

	for _, seed := range seeds {
		for _, demand := range demands {
			for _, n := range drivers {
				name := fmt.Sprintf("seed=%d/demand=%s/drivers=%d", seed, demand, n)
				t.Run(name, func(t *testing.T) {
					result := RunComparison(ScenarioFor(seed, n, demand))
					assertNoServiceRegression(t, result)
					if demand == DemandRush {
						assertNoRushMetricRegression(t, result)
					}
				})
			}
		}
	}
}

func assertNoServiceRegression(t *testing.T, result ComparisonResult) {
	t.Helper()
	b, o := result.Baseline, result.Optimized
	if o.CompletedDeliveries < b.CompletedDeliveries {
		t.Fatalf("optimized completed fewer deliveries: %d < %d", o.CompletedDeliveries, b.CompletedDeliveries)
	}
	if o.UnassignedOrders > b.UnassignedOrders {
		t.Fatalf("optimized left more unassigned: %d > %d", o.UnassignedOrders, b.UnassignedOrders)
	}
	if o.ServedFraction < b.ServedFraction {
		t.Fatalf("optimized served fraction regressed: %.3f < %.3f", o.ServedFraction, b.ServedFraction)
	}
}

func assertNoRushMetricRegression(t *testing.T, result ComparisonResult) {
	t.Helper()
	b, o := result.Baseline, result.Optimized
	if o.AveragePickupTime > b.AveragePickupTime {
		t.Fatalf("optimized avg pickup regressed under rush: %.2f > %.2f", o.AveragePickupTime, b.AveragePickupTime)
	}
	if o.P95PickupTime > b.P95PickupTime {
		t.Fatalf("optimized p95 pickup regressed under rush: %.2f > %.2f", o.P95PickupTime, b.P95PickupTime)
	}
	if o.TotalDistance > b.TotalDistance {
		t.Fatalf("optimized distance regressed under rush: %.1f > %.1f", o.TotalDistance, b.TotalDistance)
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

// TestProbeFixedMetric prints baseline vs optimized on the fair pickup metric
// for a few representative cells. Before-adaptive numbers (BatchWindow timer)
// were recorded in session notes; this helper remains for -v debugging.
func TestProbeFixedMetricBeforeAdaptive(t *testing.T) {
	cells := []struct {
		demand  DemandLevel
		drivers int
	}{
		{DemandRush, 8},
		{DemandLight, 12},
		{DemandSteady, 4},
	}
	for _, c := range cells {
		r := RunComparison(ScenarioFor(42, c.drivers, c.demand))
		line := fmt.Sprintf(
			"PROBE seed=42 demand=%s drivers=%d | baseline avg=%.2f p95=%.2f completed=%d unassigned=%d served=%.3f | optimized avg=%.2f p95=%.2f completed=%d unassigned=%d served=%.3f",
			c.demand, c.drivers,
			r.Baseline.AveragePickupTime, r.Baseline.P95PickupTime, r.Baseline.CompletedDeliveries, r.Baseline.UnassignedOrders, r.Baseline.ServedFraction,
			r.Optimized.AveragePickupTime, r.Optimized.P95PickupTime, r.Optimized.CompletedDeliveries, r.Optimized.UnassignedOrders, r.Optimized.ServedFraction,
		)
		t.Log(line)
		fmt.Fprintln(os.Stderr, line)
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
	if result.Baseline.ServedFraction <= 0 || result.Optimized.ServedFraction <= 0 {
		t.Fatal("expected a positive served fraction")
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
