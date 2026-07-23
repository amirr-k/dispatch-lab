// Package city generates deterministic road graphs for simulations.
package city

import (
	"fmt"
	"math"
	"math/rand"

	"dispatchlab/internal/domain"
)

// GridConfig controls the shape of a generated grid city.
type GridConfig struct {
	Seed        int64
	Rows        int
	Cols        int
	CellSpacing float64
	// JitterFraction perturbs node coordinates by up to this fraction of
	// CellSpacing, so the map doesn't look like a perfect lattice.
	JitterFraction float64
}

// DefaultGridConfig is a small city sized for the first vertical slice.
func DefaultGridConfig(seed int64) GridConfig {
	return GridConfig{
		Seed:           seed,
		Rows:           6,
		Cols:           6,
		CellSpacing:    100,
		JitterFraction: 0.15,
	}
}

// GenerateGrid deterministically builds a grid-shaped city graph for the
// given config. The same config always produces the same graph.
func GenerateGrid(cfg GridConfig) *domain.City {
	rng := rand.New(rand.NewSource(cfg.Seed))

	nodes := make(map[domain.NodeID]domain.Node, cfg.Rows*cfg.Cols)
	edges := make(map[domain.NodeID][]domain.Edge)

	nodeID := func(r, c int) domain.NodeID {
		return domain.NodeID(fmt.Sprintf("n-%d-%d", r, c))
	}

	for r := 0; r < cfg.Rows; r++ {
		for c := 0; c < cfg.Cols; c++ {
			jitterX := (rng.Float64()*2 - 1) * cfg.JitterFraction * cfg.CellSpacing
			jitterY := (rng.Float64()*2 - 1) * cfg.JitterFraction * cfg.CellSpacing
			id := nodeID(r, c)
			nodes[id] = domain.Node{
				ID: id,
				X:  float64(c)*cfg.CellSpacing + jitterX,
				Y:  float64(r)*cfg.CellSpacing + jitterY,
			}
		}
	}

	addEdge := func(from, to domain.NodeID) {
		a, b := nodes[from], nodes[to]
		dx, dy := a.X-b.X, a.Y-b.Y
		weight := math.Sqrt(dx*dx + dy*dy)
		id := domain.EdgeID(fmt.Sprintf("e-%s-%s", from, to))
		edges[from] = append(edges[from], domain.Edge{ID: id, From: from, To: to, Weight: weight})
	}

	for r := 0; r < cfg.Rows; r++ {
		for c := 0; c < cfg.Cols; c++ {
			id := nodeID(r, c)
			if c+1 < cfg.Cols {
				right := nodeID(r, c+1)
				addEdge(id, right)
				addEdge(right, id)
			}
			if r+1 < cfg.Rows {
				down := nodeID(r+1, c)
				addEdge(id, down)
				addEdge(down, id)
			}
		}
	}

	return &domain.City{
		ID:      fmt.Sprintf("city-%d", cfg.Seed),
		Version: 1,
		Nodes:   nodes,
		Edges:   edges,
	}
}
