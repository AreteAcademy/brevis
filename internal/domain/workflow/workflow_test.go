package workflow

import "testing"

func no(id, run string) Node { return Node{ID: id, Run: run} }

func TestValidateAcceptsASimpleChain(t *testing.T) {
	w := Workflow{
		Slug:  "daily-report",
		Kind:  KindChain,
		Nodes: []Node{no("a", "echo a"), no("b", "echo b")},
		Edges: []Edge{{From: "a", To: "b"}},
	}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRefusesADuplicateID(t *testing.T) {
	w := Workflow{Slug: "x", Nodes: []Node{no("a", "echo"), no("a", "echo")}}
	if err := w.Validate(); err == nil {
		t.Fatal("expected a duplicate-id error")
	}
}

func TestValidateRefusesAMissingDependency(t *testing.T) {
	w := Workflow{
		Slug:  "x",
		Nodes: []Node{no("a", "echo")},
		Edges: []Edge{{From: "fantasma", To: "a"}},
	}
	if err := w.Validate(); err == nil {
		t.Fatal("expected a missing-dependency error")
	}
}

// The cycle is the invariant that matters most: a cyclic graph hangs the executor
// instead of failing, and the error has to say WHICH steps form it.
func TestValidateFindsACycleAndShowsThePath(t *testing.T) {
	w := Workflow{
		Slug:  "x",
		Nodes: []Node{no("a", "e"), no("b", "e"), no("c", "e")},
		Edges: []Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "c", To: "a"}},
	}
	err := w.Validate()
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	if got := err.Error(); !contains(got, "a -> b -> c -> a") {
		t.Errorf("error = %q; wanted the cycle's path", got)
	}
}

func TestValidateRefusesASelfDependency(t *testing.T) {
	w := Workflow{Slug: "x", Nodes: []Node{no("a", "e")}, Edges: []Edge{{From: "a", To: "a"}}}
	if err := w.Validate(); err == nil {
		t.Fatal("expected a self-dependency error")
	}
}

func TestValidateRequiresExactlyOneWayToRun(t *testing.T) {
	casos := map[string]Node{
		"neither run nor action": {ID: "a"},
		"run e action":           {ID: "a", Run: "echo", Action: "docker.run"},
		"with, no action":        {ID: "a", Run: "echo", With: map[string]any{"image": "x"}},
	}
	for nome, n := range casos {
		t.Run(nome, func(t *testing.T) {
			w := Workflow{Slug: "x", Nodes: []Node{n}}
			if err := w.Validate(); err == nil {
				t.Fatalf("expected an error for %q", nome)
			}
		})
	}
}

func TestValidateRefusesAnEmptyWorkflow(t *testing.T) {
	if err := (Workflow{Slug: "x"}).Validate(); err == nil {
		t.Fatal("expected an error for a workflow with no steps")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
