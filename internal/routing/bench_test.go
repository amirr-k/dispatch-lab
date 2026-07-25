package routing

import (
	"fmt"
	"sort"
	"testing"

	"dispatchlab/internal/city"
	"dispatchlab/internal/domain"
)

// graphSizes spans the demo city (6x6) up to a graph far larger than the
// product ever renders, so the growth curve is visible rather than a single
// point measurement.
var graphSizes = []int{6, 20, 50, 100}

func benchCity(side int) *domain.City {
	return city.GenerateGrid(city.GridConfig{
		Seed:           1,
		Rows:           side,
		Cols:           side,
		CellSpacing:    100,
		JitterFraction: 0.15,
	})
}

// BenchmarkFindRouteByGraphSize routes corner to corner, the longest path the
// graph admits, so the measurement is a worst case rather than an average
// over pairs that happen to be adjacent.
func BenchmarkFindRouteByGraphSize(b *testing.B) {
	for _, side := range graphSizes {
		c := benchCity(side)
		start := domain.NodeID("n-0-0")
		goal := domain.NodeID(fmt.Sprintf("n-%d-%d", side-1, side-1))

		if _, ok := FindRoute(c, start, goal); !ok {
			b.Fatalf("%dx%d: corner to corner is unroutable", side, side)
		}

		b.Run(fmt.Sprintf("%dx%d_nodes=%d", side, side, side*side), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, ok := FindRoute(c, start, goal); !ok {
					b.Fatal("route disappeared mid-benchmark")
				}
			}
		})
	}
}

// BenchmarkFindRouteShortHop is the common case in a running simulation: a
// driver a few blocks from its pickup. Reported separately because the
// corner-to-corner number would otherwise overstate real dispatch cost.
func BenchmarkFindRouteShortHop(b *testing.B) {
	for _, side := range graphSizes {
		if side < 6 {
			continue
		}
		c := benchCity(side)
		start := domain.NodeID("n-0-0")
		goal := domain.NodeID("n-3-3")

		b.Run(fmt.Sprintf("%dx%d_nodes=%d", side, side, side*side), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, ok := FindRoute(c, start, goal); !ok {
					b.Fatal("short hop is unroutable")
				}
			}
		})
	}
}

// BenchmarkFindRouteWithClosures measures the same corner-to-corner route
// once a fraction of the roads are shut, which is what the closure feature
// does to the search in practice - more of the frontier is dead end.
func BenchmarkFindRouteWithClosures(b *testing.B) {
	const side = 50

	for _, closedPct := range []int{0, 10, 25} {
		c := benchCity(side)
		closeFraction(c, closedPct)

		start := domain.NodeID("n-0-0")
		goal := domain.NodeID(fmt.Sprintf("n-%d-%d", side-1, side-1))
		if _, ok := FindRoute(c, start, goal); !ok {
			b.Fatalf("%d%% closed: corner to corner is unroutable", closedPct)
		}

		b.Run(fmt.Sprintf("closed=%d%%", closedPct), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				FindRoute(c, start, goal)
			}
		})
	}
}

// closeFraction shuts every nth edge, walking nodes in sorted order so the
// same percentage always closes the same roads.
func closeFraction(c *domain.City, pct int) {
	if pct <= 0 {
		return
	}
	step := 100 / pct

	ids := make([]domain.NodeID, 0, len(c.Nodes))
	for id := range c.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	seen := 0
	for _, id := range ids {
		for _, e := range c.Edges[id] {
			seen++
			if seen%step == 0 {
				c.SetClosed(e.ID, true)
			}
		}
	}
}
