package workflow

import (
	"os"
	"strings"
	"testing"

	dominio "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// The example the author asked for has to pass exactly as written.
func TestParseExemploDailyReport(t *testing.T) {
	b, err := os.ReadFile("../../../examples/daily-report.yaml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := Parse("daily-report.yaml", b)
	if err != nil {
		t.Fatal(err)
	}

	if w.Slug != "daily-report" {
		t.Errorf("slug = %q; with no `name` it has to come from the file name", w.Slug)
	}
	if w.Schedule != "0 2 * * *" {
		t.Errorf("schedule = %q", w.Schedule)
	}
	if len(w.Nodes) != 3 {
		t.Fatalf("nodes = %d, wanted 3", len(w.Nodes))
	}
	// chain vira arestas: o motor so ve DAG
	if len(w.Edges) != 2 {
		t.Fatalf("edges = %d, wanted 2 (a 3-step chain)", len(w.Edges))
	}
	if w.Edges[0] != (dominio.Edge{From: "fetch_data", To: "build_report"}) {
		t.Errorf("first aresta = %+v", w.Edges[0])
	}

	docker := w.Nodes[1]
	if docker.Action != "docker.run" {
		t.Errorf("action = %q", docker.Action)
	}
	if docker.With["image"] != "ghcr.io/acme/reporting:1.4.2" {
		t.Errorf("with.image = %v", docker.With["image"])
	}
}

// Fan-out and fan-in: the same engine, with declared dependencies.
func TestParseDAGWithParallelism(t *testing.T) {
	b, err := os.ReadFile("../../../examples/analytics-dag.yaml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := Parse("analytics-dag.yaml", b)
	if err != nil {
		t.Fatal(err)
	}
	if w.Slug != "daily_analytics" {
		t.Errorf("slug = %q; it has to come from the `name` field", w.Slug)
	}
	if len(w.Edges) != 5 {
		t.Errorf("edges = %d, wanted 5", len(w.Edges))
	}
}

func TestParseRefusesDependsOnInAChain(t *testing.T) {
	y := []byte("type: chain\nsteps:\n  - id: a\n    run: echo\n  - id: b\n    run: echo\n    depends_on: [a]\n")
	_, err := Parse("x.yaml", y)
	if err == nil {
		t.Fatal("expected an error: in a chain the order is the file's")
	}
}

func TestParseRefusesAnUnknownType(t *testing.T) {
	y := []byte("type: pipeline\nsteps:\n  - id: a\n    run: echo\n")
	if _, err := Parse("x.yaml", y); err == nil {
		t.Fatal("expected an unknown-type error")
	}
}

// With no `type`, it assumes DAG: with no depends_on the steps are loose and run
// in parallel. `chain` imposes an order and for that reason has to be asked
// for.
func TestParseWithNoTypeAssumesDAG(t *testing.T) {
	y := []byte("steps:\n  - id: a\n    run: echo\n  - id: b\n    run: echo\n")
	w, err := Parse("x.yaml", y)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != dominio.KindDAG {
		t.Errorf("kind = %q, wanted dag", w.Kind)
	}
	if len(w.Edges) != 0 {
		t.Errorf("edges = %d; with no depends_on the steps are independent", len(w.Edges))
	}
}

func TestParseNamesTheFileInTheError(t *testing.T) {
	y := []byte("type: chain\nsteps:\n  - id: a\n")
	_, err := Parse("relatorio.yaml", y)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); len(got) < 15 || got[:14] != "relatorio.yaml" {
		t.Errorf("error = %q; it has to start with the file", got)
	}
}

// The two declared fields, end to end from the file.
func TestRuntimeAndToolsComeOffTheFile(t *testing.T) {
	w, err := Parse("x.yaml", []byte(`
name: x
type: dag
steps:
  - id: transform
    run: /opt/run.sh
    image: ghcr.io/acme/runner:1
    runtime: PYTHON
    tools: [" dbt ", Spark]
`))
	if err != nil {
		t.Fatal(err)
	}
	n := w.Nodes[0]
	// Normalized: the vocabulary is lowercase, and a file that shouts should
	// not become a workflow that fails to validate.
	if n.Runtime != "python" {
		t.Errorf("runtime = %q, want python", n.Runtime)
	}
	if len(n.Tools) != 2 || n.Tools[0] != "dbt" || n.Tools[1] != "spark" {
		t.Errorf("tools = %v, want [dbt spark]", n.Tools)
	}
}

// An id outside the vocabulary is refused at PUBLISH, naming what is valid.
//
// Dropping it instead would render as a missing chip on a screen three days
// later, with nothing to trace it to.
func TestAnUnknownRuntimeIsRefusedNamingTheValidOnes(t *testing.T) {
	_, err := Parse("x.yaml", []byte(`
name: x
type: dag
steps:
  - id: a
    run: echo hi
    runtime: cobol
`))
	if err == nil {
		t.Fatal("cobol was accepted")
	}
	for _, want := range []string{"cobol", "python", "go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestAnUnknownToolIsRefused(t *testing.T) {
	_, err := Parse("x.yaml", []byte(`
name: x
type: dag
steps:
  - id: a
    run: echo hi
    tools: [dbt, luigi]
`))
	if err == nil || !strings.Contains(err.Error(), "luigi") {
		t.Fatalf("err = %v; want a refusal naming luigi", err)
	}
}

// A step that declares neither is the NORMAL case, and it has to stay
// indistinguishable from a workflow written before the fields existed.
func TestDeclaringNeitherLeavesTheStepAsItWas(t *testing.T) {
	w, err := Parse("x.yaml", []byte(`
name: x
type: dag
steps:
  - id: a
    run: python fetch.py
`))
	if err != nil {
		t.Fatal(err)
	}
	if n := w.Nodes[0]; n.Runtime != "" || n.Tools != nil {
		t.Errorf("got %+v, want both empty -- the engine infers, it does not "+
			"write the inference back into the definition", n)
	}
}

// A workflow's `description` reaches the domain, trimmed.
//
// Trimmed because `description: >` -- how anybody writes more than one sentence
// in YAML -- leaves a trailing newline, and a blank line at the end of a
// paragraph is a gap on the screen nobody put there.
func TestTheDescriptionCrossesFromTheYAML(t *testing.T) {
	w, err := Parse("vendas.yaml", []byte(`
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
schedule: "0 4 * * *"
steps:
  - id: extract
    run: python fetch.py
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !strings.HasPrefix(w.Description, "Pulls yesterday's orders") {
		t.Errorf("Description = %q", w.Description)
	}
	if !strings.Contains(w.Description, "closes its books at 03:30") {
		t.Errorf("the second sentence was lost: %q", w.Description)
	}
	if strings.HasSuffix(w.Description, "\n") {
		t.Errorf("Description keeps a trailing newline: %q", w.Description)
	}
}

// A workflow with no description gets the empty string, which is what the
// screen reads as "draw nothing". A heading with no text under it looks like
// something that failed to load.
func TestNoDescriptionIsEmptyAndNotAPlaceholder(t *testing.T) {
	w, err := Parse("vendas.yaml", []byte("steps:\n  - id: extract\n    run: python fetch.py\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if w.Description != "" {
		t.Errorf("Description = %q on a workflow that declares none", w.Description)
	}
}
