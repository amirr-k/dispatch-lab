package matching

import (
	"math"
	"sort"
)

// Unreachable is the sentinel cost callers should use for a pair that
// cannot actually be matched (e.g. no route exists). It's a large finite
// value rather than +Inf so the algorithm's internal arithmetic never
// produces NaN; MinCostAssignment may still be forced to return a pair at
// this cost if there's truly no better option, so callers should treat any
// result with Cost >= Unreachable as not a real assignment.
const Unreachable = 1e12

// Pair is one matched (row, col) with its cost from the original matrix.
type Pair struct {
	Row, Col int
	Cost     float64
}

// MinCostAssignment solves min-cost bipartite assignment over a rectangular
// cost matrix (rows workers, cols jobs), matching min(rows, cols) pairs so
// every worker is assigned if rows<=cols, or every job is filled if
// cols<rows, minimizing total cost. It uses the Hungarian algorithm
// (Kuhn-Munkres) with potentials, O(n^3) where n = max(rows, cols), and is
// fully deterministic: the same matrix always produces the same pairing.
//
// cost must be rectangular (every row the same length); an empty matrix
// returns nil.
func MinCostAssignment(cost [][]float64) []Pair {
	rows := len(cost)
	if rows == 0 {
		return nil
	}
	cols := len(cost[0])
	if cols == 0 {
		return nil
	}

	original := cost
	work := cost
	transposed := false
	if rows > cols {
		work = transpose(cost)
		rows, cols = cols, rows
		transposed = true
	}

	// classic O(n^3) Hungarian algorithm with row/col potentials (u, v),
	// 1-indexed to match the standard formulation and avoid off-by-one bugs
	// in the augmenting-path bookkeeping.
	const inf = math.MaxFloat64 / 4
	u := make([]float64, rows+1)
	v := make([]float64, cols+1)
	p := make([]int, cols+1) // p[j] = row matched to column j, 0 = unmatched
	way := make([]int, cols+1)

	for i := 1; i <= rows; i++ {
		p[0] = i
		j0 := 0
		minv := make([]float64, cols+1)
		used := make([]bool, cols+1)
		for j := range minv {
			minv[j] = inf
		}

		for {
			used[j0] = true
			i0 := p[j0]
			delta := inf
			j1 := -1
			for j := 1; j <= cols; j++ {
				if used[j] {
					continue
				}
				cur := work[i0-1][j-1] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j] = cur
					way[j] = j0
				}
				if minv[j] < delta {
					delta = minv[j]
					j1 = j
				}
			}
			for j := 0; j <= cols; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for j0 != 0 {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
		}
	}

	pairs := make([]Pair, 0, rows)
	for j := 1; j <= cols; j++ {
		if p[j] == 0 {
			continue
		}
		r, c := p[j]-1, j-1
		if transposed {
			r, c = c, r
		}
		pairs = append(pairs, Pair{Row: r, Col: c, Cost: original[r][c]})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Row < pairs[j].Row })
	return pairs
}

func transpose(m [][]float64) [][]float64 {
	rows, cols := len(m), len(m[0])
	t := make([][]float64, cols)
	for j := range t {
		t[j] = make([]float64, rows)
		for i := range m {
			t[j][i] = m[i][j]
		}
	}
	return t
}
