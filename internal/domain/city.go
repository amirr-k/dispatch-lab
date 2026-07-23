package domain

// NodeID identifies an intersection in a city graph.
type NodeID string

// EdgeID identifies a directed road segment.
type EdgeID string

// Node is an intersection with a position used for both routing heuristics
// and rendering.
type Node struct {
	ID NodeID
	X  float64
	Y  float64
}

// Edge is a directed road segment between two nodes.
type Edge struct {
	ID     EdgeID
	From   NodeID
	To     NodeID
	Weight float64
	Closed bool
}

// City is a versioned road graph.
type City struct {
	ID      string
	Version int
	Nodes   map[NodeID]Node
	// Edges is adjacency-list form: from a node, the outgoing edges.
	Edges map[NodeID][]Edge
}

// EdgeByID returns the edge with the given ID and whether it was found.
// Used by closure commands, which address an edge by ID rather than endpoints.
func (c *City) EdgeByID(id EdgeID) (Edge, bool) {
	for _, edges := range c.Edges {
		for _, e := range edges {
			if e.ID == id {
				return e, true
			}
		}
	}
	return Edge{}, false
}

// SetClosed toggles the closed state of an edge by ID and reports whether it existed.
func (c *City) SetClosed(id EdgeID, closed bool) bool {
	for from, edges := range c.Edges {
		for i, e := range edges {
			if e.ID == id {
				c.Edges[from][i].Closed = closed
				return true
			}
		}
	}
	return false
}
