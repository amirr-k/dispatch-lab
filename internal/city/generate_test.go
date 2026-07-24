package city

import (
	"math"
	"reflect"
	"testing"

	"dispatchlab/internal/domain"
)

func TestGenerateGridDeterministic(t *testing.T) {
	cfg := DefaultGridConfig(42)
	a := GenerateGrid(cfg)
	b := GenerateGrid(cfg)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same config produced different graphs")
	}
}

func TestGenerateGridDifferentSeed(t *testing.T) {
	a := GenerateGrid(DefaultGridConfig(1))
	b := GenerateGrid(DefaultGridConfig(2))

	same := true
	for id, na := range a.Nodes {
		nb := b.Nodes[id]
		if na.X != nb.X || na.Y != nb.Y {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds produced identical node coordinates")
	}
}

func TestGenerateGridNodeCount(t *testing.T) {
	cfg := GridConfig{Seed: 7, Rows: 4, Cols: 5, CellSpacing: 100, JitterFraction: 0.1}
	c := GenerateGrid(cfg)
	if len(c.Nodes) != cfg.Rows*cfg.Cols {
		t.Fatalf("expected %d nodes, got %d", cfg.Rows*cfg.Cols, len(c.Nodes))
	}
}

func TestGenerateGridEdgeCount(t *testing.T) {
	cfg := GridConfig{Seed: 7, Rows: 4, Cols: 5, CellSpacing: 100, JitterFraction: 0.1}
	c := GenerateGrid(cfg)

	// each interior grid connection is added in both directions
	want := 2 * (cfg.Rows*(cfg.Cols-1) + (cfg.Rows-1)*cfg.Cols)
	got := 0
	for _, list := range c.Edges {
		got += len(list)
	}
	if got != want {
		t.Fatalf("expected %d directed edges, got %d", want, got)
	}
}

func TestGenerateGridEdgesAreValid(t *testing.T) {
	c := GenerateGrid(DefaultGridConfig(3))

	for from, list := range c.Edges {
		for _, e := range list {
			if _, ok := c.Nodes[e.From]; !ok {
				t.Fatalf("edge %s references missing From node %s", e.ID, e.From)
			}
			if _, ok := c.Nodes[e.To]; !ok {
				t.Fatalf("edge %s references missing To node %s", e.ID, e.To)
			}
			if e.From != from {
				t.Fatalf("edge %s stored under wrong adjacency key %s", e.ID, from)
			}

			a, b := c.Nodes[e.From], c.Nodes[e.To]
			want := math.Hypot(a.X-b.X, a.Y-b.Y)
			if math.Abs(e.Weight-want) > 1e-9 {
				t.Fatalf("edge %s weight %v != euclidean %v", e.ID, e.Weight, want)
			}
		}
	}
}

func TestGenerateGridIsBidirectional(t *testing.T) {
	c := GenerateGrid(DefaultGridConfig(9))

	for _, list := range c.Edges {
		for _, e := range list {
			reverse, ok := findEdge(c, e.To, e.From)
			if !ok {
				t.Fatalf("edge %s->%s has no reverse", e.From, e.To)
			}
			if math.Abs(reverse.Weight-e.Weight) > 1e-9 {
				t.Fatalf("reverse of %s has mismatched weight", e.ID)
			}
		}
	}
}

func findEdge(c *domain.City, from, to domain.NodeID) (domain.Edge, bool) {
	for _, e := range c.Edges[from] {
		if e.To == to {
			return e, true
		}
	}
	return domain.Edge{}, false
}
