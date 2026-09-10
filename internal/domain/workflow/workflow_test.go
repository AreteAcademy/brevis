package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

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
	for name, n := range casos {
		t.Run(name, func(t *testing.T) {
			w := Workflow{Slug: "x", Nodes: []Node{n}}
			if err := w.Validate(); err == nil {
				t.Fatalf("expected an error for %q", name)
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

// A definition published BEFORE these fields existed has to keep working.
//
// A Workflow is stored whole as JSON in workflows.definicao and runs.definicao
// with no tags, so the Go field name is the key. Adding fields is additive in
// both directions, and this is what says so rather than assuming it: a document
// with no Runtime and no Tools reads back with both empty, which means "not
// declared" and falls through to the inference.
//
// The opposite case -- a field RENAMED under an untagged struct -- is the one
// that silently loses data, and TestTheParamKeysAreTheOnDiskFormat covers it.
func TestAnOlderStoredDefinitionStillReads(t *testing.T) {
	// Exactly what the database holds for a workflow published before this
	// change: no Runtime, no Tools.
	stored := `{"Slug":"w","Name":"W","Kind":"dag","Nodes":[
		{"ID":"a","Run":"python fetch.py","Image":"python:3.12"}],"Edges":[]}`

	var w Workflow
	if err := json.Unmarshal([]byte(stored), &w); err != nil {
		t.Fatalf("an older document no longer decodes: %v", err)
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("an older document no longer validates: %v", err)
	}
	if n := w.Nodes[0]; n.Runtime != "" || n.Tools != nil {
		t.Errorf("got %+v, want both empty", n)
	}

	// And the new fields survive the round trip they will now take.
	w.Nodes[0].Runtime = "python"
	w.Nodes[0].Tools = []string{"dbt"}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var back Workflow
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Nodes[0].Runtime != "python" || len(back.Nodes[0].Tools) != 1 {
		t.Errorf("the round trip lost the declaration: %+v", back.Nodes[0])
	}
}

// `host:` is an alternative to `image:`, and the pair is refused at PUBLISH.
//
// The alternative is a step that publishes fine, sits in the schedule, and
// fails at three in the morning on whichever executor happened to win. Refusing
// here means the person who wrote it is the person who reads the message.
func TestAHostAndAnImageAreAlternatives(t *testing.T) {
	for name, node := range map[string]Node{
		"both host and image": {
			ID: "extract", Run: "python x.py",
			Host: "dlt-runner-01", Image: "python:3.12",
		},
		// An action resolves in the engine's own Go registry and cannot cross a
		// machine, so `host:` with `action:` is a file asking for something
		// that does not exist.
		"host with an action": {
			ID: "extract", Action: "http.get", Host: "dlt-runner-01",
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := Workflow{Slug: "vendas", Nodes: []Node{node}}
			err := w.Validate()
			if err == nil {
				t.Fatalf("%+v was accepted", node)
			}
			if !strings.Contains(err.Error(), "host") {
				t.Errorf("the message does not name the field: %v", err)
			}
		})
	}

	// And a host on its own is fine, which is the whole point.
	ok := Workflow{Slug: "vendas", Nodes: []Node{
		{ID: "extract", Run: "python pipelines/orders.py", Host: "dlt-runner-01"},
	}}
	if err := ok.Validate(); err != nil {
		t.Errorf("a plain host step was refused: %v", err)
	}
}
