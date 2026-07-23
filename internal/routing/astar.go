// Package routing computes shortest paths through a city graph.
package routing

import (
	"container/heap"
	"math"

	"dispatchlab/internal/domain"
)

// Route is a computed path through the city graph.
type Route struct {
	Nodes    []domain.NodeID
	Distance float64
}

// FindRoute computes the shortest path from start to goal using A* with a
// Euclidean-distance heuristic, which is admissible because edge weights are
// themselves Euclidean distances (or greater, never less).
// Closed edges are treated as impassable. Returns ok=false if no path exists.
func FindRoute(city *domain.City, start, goal domain.NodeID) (Route, bool) {
	if start == goal {
		return Route{Nodes: []domain.NodeID{start}, Distance: 0}, true
	}
	if _, ok := city.Nodes[start]; !ok {
		return Route{}, false
	}
	if _, ok := city.Nodes[goal]; !ok {
		return Route{}, false
	}

	goalNode := city.Nodes[goal]
	heuristic := func(id domain.NodeID) float64 {
		n := city.Nodes[id]
		dx, dy := n.X-goalNode.X, n.Y-goalNode.Y
		return math.Sqrt(dx*dx + dy*dy)
	}

	gScore := map[domain.NodeID]float64{start: 0}
	cameFrom := map[domain.NodeID]domain.NodeID{}

	open := &priorityQueue{}
	heap.Init(open)
	heap.Push(open, &pqItem{node: start, priority: heuristic(start), seq: 0})

	visited := map[domain.NodeID]bool{}
	seq := 1

	for open.Len() > 0 {
		current := heap.Pop(open).(*pqItem)
		if visited[current.node] {
			continue
		}
		if current.node == goal {
			return reconstruct(cameFrom, gScore, start, goal), true
		}
		visited[current.node] = true

		for _, edge := range city.Edges[current.node] {
			if edge.Closed {
				continue
			}
			tentative := gScore[current.node] + edge.Weight
			existing, has := gScore[edge.To]
			if !has || tentative < existing {
				gScore[edge.To] = tentative
				cameFrom[edge.To] = current.node
				heap.Push(open, &pqItem{
					node:     edge.To,
					priority: tentative + heuristic(edge.To),
					seq:      seq,
				})
				seq++
			}
		}
	}

	return Route{}, false
}

func reconstruct(cameFrom map[domain.NodeID]domain.NodeID, gScore map[domain.NodeID]float64, start, goal domain.NodeID) Route {
	path := []domain.NodeID{goal}
	for path[len(path)-1] != start {
		prev := cameFrom[path[len(path)-1]]
		path = append(path, prev)
	}
	// reverse
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return Route{Nodes: path, Distance: gScore[goal]}
}

// pqItem is an entry in the A* open set. seq is a tie-breaker so equal
// priority nodes are ordered deterministically by insertion order.
type pqItem struct {
	node     domain.NodeID
	priority float64
	seq      int
	index    int
}

type priorityQueue []*pqItem

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	if pq[i].priority != pq[j].priority {
		return pq[i].priority < pq[j].priority
	}
	return pq[i].seq < pq[j].seq
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	item := x.(*pqItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	return item
}
