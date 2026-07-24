package domain

import "testing"

func twoNodeCity() *City {
	return &City{
		ID:      "test",
		Version: 1,
		Nodes: map[NodeID]Node{
			"a": {ID: "a", X: 0, Y: 0},
			"b": {ID: "b", X: 1, Y: 0},
		},
		Edges: map[NodeID][]Edge{
			"a": {{ID: "e-a-b", From: "a", To: "b", Weight: 1}},
			"b": {{ID: "e-b-a", From: "b", To: "a", Weight: 1}},
		},
	}
}

func TestEdgeByID(t *testing.T) {
	c := twoNodeCity()

	e, ok := c.EdgeByID("e-a-b")
	if !ok {
		t.Fatal("expected e-a-b to be found")
	}
	if e.From != "a" || e.To != "b" {
		t.Fatalf("unexpected edge endpoints: %+v", e)
	}

	if _, ok := c.EdgeByID("missing"); ok {
		t.Fatal("expected missing edge to report not found")
	}
}

func TestSetClosed(t *testing.T) {
	c := twoNodeCity()

	if ok := c.SetClosed("e-a-b", true); !ok {
		t.Fatal("expected SetClosed to report the edge existed")
	}
	e, _ := c.EdgeByID("e-a-b")
	if !e.Closed {
		t.Fatal("edge should be closed after SetClosed(true)")
	}

	if ok := c.SetClosed("e-a-b", false); !ok {
		t.Fatal("expected reopen to report the edge existed")
	}
	e, _ = c.EdgeByID("e-a-b")
	if e.Closed {
		t.Fatal("edge should be open after SetClosed(false)")
	}

	if ok := c.SetClosed("missing", true); ok {
		t.Fatal("expected SetClosed on a missing edge to report false")
	}
}
