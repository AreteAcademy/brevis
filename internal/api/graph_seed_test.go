package api_test

import (
	"encoding/json"
	"html"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// seedOf is the graph a page embeds in #dag, decoded.
func seedOf(t *testing.T, body string) map[string]any {
	t.Helper()
	at := strings.Index(body, `<script type="application/json" data-graph>`)
	if at < 0 {
		t.Fatal("the page embeds no graph")
	}
	raw := body[at+len(`<script type="application/json" data-graph>`):]
	raw = html.UnescapeString(raw[:strings.Index(raw, "</script>")])
	var g map[string]any
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatalf("the embedded graph is not JSON: %v", err)
	}
	return g
}

func same(t *testing.T, embedded map[string]any, api graph) {
	t.Helper()
	nodes, _ := embedded["nodes"].([]any)
	edges, _ := embedded["edges"].([]any)
	if len(nodes) != len(api.Nodes) || len(edges) != len(api.Edges) {
		t.Errorf("the embedded graph has %d nodes and %d edges, the API %d and %d",
			len(nodes), len(edges), len(api.Nodes), len(api.Edges))
	}
}

// THE GRAPH IS IN THE FIRST PAINT (nav-latency NL5). The page embeds the
// graph the island used to fetch after the load -- one round trip that drew an
// empty canvas, then popped the graph in -- built by the same code as
// /api/.../graph, so the two cannot differ. dag.js draws from it on mount and
// fetches only to refresh a live run.
func TestTheWorkflowPageEmbedsItsGraph(t *testing.T) {
	ui := newUIWithReads(defsFake{w: diamond()}, nil)
	_, body := get(t, ui, "/workflows/diamond")
	_, g := request(t, ui, "/api/workflows/diamond/graph")
	same(t, seedOf(t, body), g)
}

func TestTheRunPageEmbedsItsGraph(t *testing.T) {
	id := uuid.New()
	def, _ := json.Marshal(diamond())
	e := execsFake{
		run:    dom.Run{ID: id, WorkflowSlug: "diamond", Status: dom.StatusRunning, Definition: def},
		states: map[string]postgres.NodeState{"a": {Status: string(dom.StatusSuccess)}},
	}
	ui := newUIWithReads(defsFake{w: diamond()}, e)
	_, body := get(t, ui, "/runs/"+id.String())
	_, g := request(t, ui, "/api/runs/"+id.String()+"/graph")
	seed := seedOf(t, body)
	same(t, seed, g)
	if seed["run_id"] != id.String() || seed["terminal"] != false {
		t.Errorf("the embedded run graph lost its run or its state: %v %v", seed["run_id"], seed["terminal"])
	}

	_, raw := get(t, ui, "/assets/dag.js")
	for _, want := range []string{"script[data-graph]", "seed"} {
		if !strings.Contains(strip(raw), want) {
			t.Errorf("dag.js does not draw from the embedded graph (%s)", want)
		}
	}
}

// newUIWithReads is a console with definitions, runs and the reads a
// workflow's and a run's pages make.
func newUIWithReads(d api.Definitions, e api.RunsChart) *api.UI {
	return api.NewUI(workflowReads{readsFake: readsFake{}}, d, e, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}
