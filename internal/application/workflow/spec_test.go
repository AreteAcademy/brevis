package workflow

import (
	"os"
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
