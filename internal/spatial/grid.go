// Package spatial indexes 2D points for fast nearby-candidate lookups,
// letting matching filter drivers to a bounded candidate set instead of
// comparing every order against every driver.
package spatial

import (
	"math"
	"sort"
)

// Point is a plain 2D coordinate, decoupled from the city graph's NodeID so
// this index can be built, tested, and reasoned about independently of the
// domain package.
type Point struct{ X, Y float64 }

type cellKey struct{ cx, cy int }

// Grid buckets ids into uniform square cells and answers nearby-candidate
// queries by scanning outward ring by ring from the query's cell.
//
// A uniform grid is the right choice here because driver density across a
// generated city is roughly even and queries expand outward from a fixed
// point in fixed cell steps. A k-d tree would pay off instead if drivers
// clustered heavily in some regions and were sparse in others (a uniform
// grid wastes empty cells there), or if arbitrary-radius nearest-neighbor
// queries were needed rather than expanding-ring cell scans.
type Grid struct {
	cellSize float64
	cells    map[cellKey]map[string]Point
	// points tracks each id's current position so Set/Remove can find and
	// clear its previous cell without a linear scan.
	points map[string]Point
}

// NewGrid builds an empty grid with the given cell size. A non-positive
// size is replaced with 1 rather than causing a division by zero.
func NewGrid(cellSize float64) *Grid {
	if cellSize <= 0 {
		cellSize = 1
	}
	return &Grid{
		cellSize: cellSize,
		cells:    make(map[cellKey]map[string]Point),
		points:   make(map[string]Point),
	}
}

// Set inserts id at position p, or moves it there if already indexed.
func (g *Grid) Set(id string, p Point) {
	g.Remove(id)
	g.points[id] = p
	key := g.cellOf(p)
	if g.cells[key] == nil {
		g.cells[key] = make(map[string]Point)
	}
	g.cells[key][id] = p
}

// Remove deletes id from the index. A no-op if id isn't indexed.
func (g *Grid) Remove(id string) {
	old, ok := g.points[id]
	if !ok {
		return
	}
	key := g.cellOf(old)
	delete(g.cells[key], id)
	if len(g.cells[key]) == 0 {
		delete(g.cells, key)
	}
	delete(g.points, id)
}

// Len reports how many ids are currently indexed.
func (g *Grid) Len() int { return len(g.points) }

// Candidates returns up to maxResults ids nearest query, ordered by
// distance and then by id for a deterministic tie-break. It searches the
// query's cell first and expands outward one ring at a time until enough
// candidates are found or every indexed id has been considered.
func (g *Grid) Candidates(query Point, maxResults int) []string {
	if maxResults <= 0 || len(g.points) == 0 {
		return nil
	}

	origin := g.cellOf(query)
	found := make(map[string]Point)

	for radius := 0; len(found) < maxResults && len(found) < len(g.points); radius++ {
		for cx := origin.cx - radius; cx <= origin.cx+radius; cx++ {
			for cy := origin.cy - radius; cy <= origin.cy+radius; cy++ {
				// beyond radius 0, skip the inner square already covered by
				// a smaller ring so each cell is scanned exactly once
				onRing := radius == 0 ||
					cx == origin.cx-radius || cx == origin.cx+radius ||
					cy == origin.cy-radius || cy == origin.cy+radius
				if !onRing {
					continue
				}
				for id, p := range g.cells[cellKey{cx, cy}] {
					found[id] = p
				}
			}
		}
	}

	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		di, dj := distSq(found[ids[i]], query), distSq(found[ids[j]], query)
		if di != dj {
			return di < dj
		}
		return ids[i] < ids[j]
	})
	if len(ids) > maxResults {
		ids = ids[:maxResults]
	}
	return ids
}

func (g *Grid) cellOf(p Point) cellKey {
	return cellKey{
		cx: int(math.Floor(p.X / g.cellSize)),
		cy: int(math.Floor(p.Y / g.cellSize)),
	}
}

func distSq(a, b Point) float64 {
	dx, dy := a.X-b.X, a.Y-b.Y
	return dx*dx + dy*dy
}
