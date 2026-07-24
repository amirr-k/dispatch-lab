package spatial

import (
	"reflect"
	"testing"
)

func TestCandidatesIncludeKnownNearbyDrivers(t *testing.T) {
	g := NewGrid(10)
	g.Set("near", Point{X: 1, Y: 1})
	g.Set("far", Point{X: 500, Y: 500})

	got := g.Candidates(Point{X: 0, Y: 0}, 1)
	if !reflect.DeepEqual(got, []string{"near"}) {
		t.Fatalf("expected [near] closest to origin, got %v", got)
	}
}

func TestCandidatesOrderedByDistanceThenID(t *testing.T) {
	g := NewGrid(10)
	g.Set("b", Point{X: 5, Y: 0})
	g.Set("a", Point{X: 5, Y: 0}) // exact tie with b: id breaks the tie
	g.Set("c", Point{X: 20, Y: 0})

	got := g.Candidates(Point{X: 0, Y: 0}, 3)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestCandidatesLimitsToMaxResults(t *testing.T) {
	g := NewGrid(10)
	for i, p := range []Point{{0, 0}, {1, 0}, {2, 0}, {3, 0}} {
		g.Set(string(rune('a'+i)), p)
	}
	got := g.Candidates(Point{X: 0, Y: 0}, 2)
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 candidates, got %d: %v", len(got), got)
	}
}

func TestSetMovesAnExistingID(t *testing.T) {
	g := NewGrid(10)
	g.Set("d1", Point{X: 0, Y: 0})
	g.Set("d1", Point{X: 1000, Y: 1000}) // move far away

	got := g.Candidates(Point{X: 0, Y: 0}, 5)
	if len(got) != 1 {
		t.Fatalf("expected the moved driver still indexed exactly once, got %v", got)
	}
	// it must be found by expanding out to its new, distant cell
	got = g.Candidates(Point{X: 1000, Y: 1000}, 1)
	if !reflect.DeepEqual(got, []string{"d1"}) {
		t.Fatalf("expected d1 near its new position, got %v", got)
	}
}

func TestRemoveDropsAnID(t *testing.T) {
	g := NewGrid(10)
	g.Set("d1", Point{X: 0, Y: 0})
	g.Remove("d1")

	if got := g.Candidates(Point{X: 0, Y: 0}, 5); len(got) != 0 {
		t.Fatalf("expected no candidates after removal, got %v", got)
	}
	if g.Len() != 0 {
		t.Fatalf("expected Len 0 after removal, got %d", g.Len())
	}
	// removing something not indexed must not panic
	g.Remove("does-not-exist")
}

func TestCandidatesEmptyGrid(t *testing.T) {
	g := NewGrid(10)
	if got := g.Candidates(Point{X: 0, Y: 0}, 5); got != nil {
		t.Fatalf("expected nil candidates from an empty grid, got %v", got)
	}
}

func TestCandidatesEmptyAreaExpandsOutward(t *testing.T) {
	g := NewGrid(1)
	// nothing within many cells of the origin; the one driver in the world
	// sits far away and must still be found by ring expansion
	g.Set("only", Point{X: 50, Y: 50})

	got := g.Candidates(Point{X: 0, Y: 0}, 1)
	if !reflect.DeepEqual(got, []string{"only"}) {
		t.Fatalf("expected ring expansion to find the only driver, got %v", got)
	}
}

func TestCandidatesAtCellBoundary(t *testing.T) {
	g := NewGrid(10)
	// x=10 falls in the next cell over from x=9.999, not the same cell as
	// a query at x=0 - candidates must still be found via the neighbor ring
	g.Set("boundary", Point{X: 10, Y: 0})

	got := g.Candidates(Point{X: 9.999, Y: 0}, 1)
	if !reflect.DeepEqual(got, []string{"boundary"}) {
		t.Fatalf("expected the boundary-adjacent point to be found, got %v", got)
	}
}

func TestCandidatesNegativeCoordinates(t *testing.T) {
	g := NewGrid(10)
	g.Set("neg", Point{X: -15, Y: -15})

	got := g.Candidates(Point{X: -14, Y: -14}, 1)
	if !reflect.DeepEqual(got, []string{"neg"}) {
		t.Fatalf("expected negative-coordinate cells to index correctly, got %v", got)
	}
}

func TestMaxResultsZeroOrNegativeReturnsNil(t *testing.T) {
	g := NewGrid(10)
	g.Set("d1", Point{X: 0, Y: 0})
	if got := g.Candidates(Point{X: 0, Y: 0}, 0); got != nil {
		t.Fatalf("expected nil for maxResults=0, got %v", got)
	}
	if got := g.Candidates(Point{X: 0, Y: 0}, -1); got != nil {
		t.Fatalf("expected nil for negative maxResults, got %v", got)
	}
}
