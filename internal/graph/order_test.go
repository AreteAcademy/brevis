package graph

import (
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

func TestNiveisEmCadeia(t *testing.T) {
	w := wf.Workflow{
		Nodes: []wf.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []wf.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	}
	n, err := Levels(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 3 {
		t.Fatalf("levels = %d, wanted 3 (a chain does not parallelize)", len(n))
	}
}

// The point of grouping by level: gold_metrics and gold_users are independent
// e devem sair juntas. Uma ordenacao topologica linear as serializaria.
func TestNiveisPreservamParalelismo(t *testing.T) {
	w := wf.Workflow{
		Nodes: []wf.Node{{ID: "silver"}, {ID: "metrics"}, {ID: "users"}, {ID: "publish"}},
		Edges: []wf.Edge{
			{From: "silver", To: "metrics"},
			{From: "silver", To: "users"},
			{From: "metrics", To: "publish"},
			{From: "users", To: "publish"},
		},
	}
	n, err := Levels(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 3 {
		t.Fatalf("levels = %v, wanted 3", n)
	}
	if len(n[1]) != 2 {
		t.Errorf("level 1 = %v, wanted metrics and users together", n[1])
	}
	if len(n[2]) != 1 || n[2][0] != "publish" {
		t.Errorf("level 2 = %v, wanted publish alone at the end", n[2])
	}
}

func TestLevelsWithNoEdgesAllRunTogether(t *testing.T) {
	w := wf.Workflow{Nodes: []wf.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	n, err := Levels(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 || len(n[0]) != 3 {
		t.Fatalf("levels = %v; with no dependency it is all one level", n)
	}
}

func TestLevelsDetectsACycleBuiltInCode(t *testing.T) {
	w := wf.Workflow{
		Nodes: []wf.Node{{ID: "a"}, {ID: "b"}},
		Edges: []wf.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
	}
	if _, err := Levels(w); err == nil {
		t.Fatal("expected a cycle error")
	}
}
