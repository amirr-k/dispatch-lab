package matching

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
)

func totalCost(pairs []Pair) float64 {
	var sum float64
	for _, p := range pairs {
		sum += p.Cost
	}
	return sum
}

// bruteForceMinCost enumerates every possible injective assignment and
// returns the minimum total cost, as an independent reference for checking
// MinCostAssignment's optimality on small matrices.
func bruteForceMinCost(cost [][]float64) float64 {
	rows, cols := len(cost), len(cost[0])
	if rows > cols {
		cost = transpose(cost)
		rows, cols = cols, rows
	}

	used := make([]bool, cols)
	best := math.Inf(1)
	var rec func(row int, total float64)
	rec = func(row int, total float64) {
		if total >= best {
			return
		}
		if row == rows {
			best = total
			return
		}
		for j := 0; j < cols; j++ {
			if used[j] {
				continue
			}
			used[j] = true
			rec(row+1, total+cost[row][j])
			used[j] = false
		}
	}
	rec(0, 0)
	return best
}

func TestMinCostAssignmentKnownOptimal(t *testing.T) {
	cost := [][]float64{
		{1, 2},
		{2, 1},
	}
	got := MinCostAssignment(cost)
	if len(got) != 2 {
		t.Fatalf("expected 2 pairs, got %d: %+v", len(got), got)
	}
	if totalCost(got) != 2 {
		t.Fatalf("expected optimal total cost 2 (identity matching), got %v: %+v", totalCost(got), got)
	}
}

func TestMinCostAssignmentMoreColsThanRows(t *testing.T) {
	cost := [][]float64{
		{5, 1, 9},
		{8, 7, 2},
	}
	got := MinCostAssignment(cost)
	if len(got) != 2 {
		t.Fatalf("expected every row assigned (2 pairs), got %d: %+v", len(got), got)
	}
	assertDistinctCols(t, got)

	want := bruteForceMinCost(cost)
	if math.Abs(totalCost(got)-want) > 1e-9 {
		t.Fatalf("expected optimal cost %v, got %v (%+v)", want, totalCost(got), got)
	}
}

func TestMinCostAssignmentMoreRowsThanCols(t *testing.T) {
	cost := [][]float64{
		{1, 9},
		{9, 1},
		{5, 5},
	}
	got := MinCostAssignment(cost)
	if len(got) != 2 {
		t.Fatalf("expected every column filled (2 pairs) when rows>cols, got %d: %+v", len(got), got)
	}
	assertDistinctRows(t, got)

	want := bruteForceMinCost(cost)
	if math.Abs(totalCost(got)-want) > 1e-9 {
		t.Fatalf("expected optimal cost %v, got %v (%+v)", want, totalCost(got), got)
	}
}

func TestMinCostAssignmentDeterministic(t *testing.T) {
	cost := [][]float64{
		{4, 1, 3},
		{2, 0, 5},
		{3, 2, 2},
	}
	a := MinCostAssignment(cost)
	b := MinCostAssignment(cost)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("expected the same matrix to always produce the same assignment, got %+v vs %+v", a, b)
	}
}

func TestMinCostAssignmentAvoidsUnreachablePairsWhenPossible(t *testing.T) {
	cost := [][]float64{
		{Unreachable, 1},
		{1, Unreachable},
	}
	got := MinCostAssignment(cost)
	for _, p := range got {
		if p.Cost >= Unreachable {
			t.Fatalf("expected a feasible assignment avoiding Unreachable pairs, got %+v", got)
		}
	}
	if totalCost(got) != 2 {
		t.Fatalf("expected total cost 2 (the only feasible pairing), got %v: %+v", totalCost(got), got)
	}
}

func TestMinCostAssignmentEmptyMatrix(t *testing.T) {
	if got := MinCostAssignment(nil); got != nil {
		t.Fatalf("expected nil for an empty matrix, got %+v", got)
	}
	if got := MinCostAssignment([][]float64{}); got != nil {
		t.Fatalf("expected nil for zero rows, got %+v", got)
	}
	if got := MinCostAssignment([][]float64{{}}); got != nil {
		t.Fatalf("expected nil for zero columns, got %+v", got)
	}
}

// TestMinCostAssignmentMatchesBruteForceRandomized is the randomized
// property test the spec calls for: across many small random matrices,
// MinCostAssignment's total cost must equal the brute-force optimum.
func TestMinCostAssignmentMatchesBruteForceRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 60; trial++ {
		rows := 1 + rng.Intn(5)
		cols := 1 + rng.Intn(5)
		cost := make([][]float64, rows)
		for i := range cost {
			cost[i] = make([]float64, cols)
			for j := range cost[i] {
				cost[i][j] = float64(rng.Intn(20))
			}
		}

		got := MinCostAssignment(cost)
		gotTotal := totalCost(got)
		wantTotal := bruteForceMinCost(cost)

		if math.Abs(gotTotal-wantTotal) > 1e-9 {
			t.Fatalf("trial %d (rows=%d cols=%d): got total %v, want %v; cost=%v result=%+v",
				trial, rows, cols, gotTotal, wantTotal, cost, got)
		}
		if rows <= cols {
			assertDistinctCols(t, got)
		} else {
			assertDistinctRows(t, got)
		}
	}
}

func assertDistinctCols(t *testing.T, pairs []Pair) {
	t.Helper()
	seen := map[int]bool{}
	for _, p := range pairs {
		if seen[p.Col] {
			t.Fatalf("expected distinct columns, got duplicate col %d in %+v", p.Col, pairs)
		}
		seen[p.Col] = true
	}
}

func assertDistinctRows(t *testing.T, pairs []Pair) {
	t.Helper()
	seen := map[int]bool{}
	for _, p := range pairs {
		if seen[p.Row] {
			t.Fatalf("expected distinct rows, got duplicate row %d in %+v", p.Row, pairs)
		}
		seen[p.Row] = true
	}
}
