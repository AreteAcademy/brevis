package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// A diamond graph: b and c depend on a, d depends on both. The shape matters
// because it is the smallest case in which a wrong layout shows -- b and c HAVE
// to come out in the same column, or the drawing contradicts what the executor
// does.
func diamond() wf.Workflow {
	return wf.Workflow{
		Slug: "diamond",
		Nodes: []wf.Node{
			{ID: "a", Run: "echo a"}, {ID: "b", Run: "echo b"},
			{ID: "c", Action: "docker.run"}, {ID: "d", Run: "echo d"},
		},
		Edges: []wf.Edge{{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"}},
	}
}

type defsFake struct {
	w   wf.Workflow
	err error
}

func (d defsFake) Definition(context.Context, string) (wf.Workflow, error) { return d.w, d.err }

type execsFake struct {
	run    dom.Run
	states map[string]postgres.NodeState
	err    error
}

func (e execsFake) Get(context.Context, uuid.UUID) (dom.Run, error) { return e.run, e.err }
func (e execsFake) LogsDaRun(context.Context, uuid.UUID) ([]postgres.StepLog, error) {
	return nil, nil
}

func (e execsFake) NodeStates(context.Context, uuid.UUID) (map[string]postgres.NodeState, error) {
	return e.states, nil
}

type graph struct {
	Slug     string `json:"slug"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Terminal bool   `json:"terminal"`
	Nodes    []struct {
		ID         string             `json:"id"`
		Type       string             `json:"type"`
		Position   struct{ X, Y int } `json:"position"`
		Data       map[string]any     `json:"data"`
		ParentID   string             `json:"parentId"`
		Extent     string             `json:"extent"`
		Style      map[string]any     `json:"style"`
		Selectable *bool              `json:"selectable"`
	} `json:"nodes"`
	Edges []struct {
		ID       string `json:"id"`
		Source   string `json:"source"`
		Target   string `json:"target"`
		Animated bool   `json:"animated"`
	} `json:"edges"`
}

func request(t *testing.T, ui *api.UI, path string) (*http.Response, graph) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	res := rec.Result()
	var g graph
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &g); err != nil {
			t.Fatalf("json invalido: %v — %s", err, body)
		}
	}
	return res, g
}

func newUI(d api.Definitions, e api.RunsChart) *api.UI {
	return api.NewUI(nil, d, e, nil, branding.Default(), slog.New(slog.DiscardHandler))
}

func TestTheWorkflowGraphPutsLevelsInColumns(t *testing.T) {
	ui := newUI(defsFake{w: diamond()}, execsFake{})
	res, g := request(t, ui, "/api/workflows/diamond/graph")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(g.Nodes) != 4 || len(g.Edges) != 4 {
		t.Fatalf("nodes=%d edges=%d, want 4 e 4", len(g.Nodes), len(g.Edges))
	}

	x := map[string]int{}
	y := map[string]int{}
	for _, n := range g.Nodes {
		x[n.ID], y[n.ID] = n.Position.X, n.Position.Y
		if n.Type != "brevis" {
			t.Errorf("node %s has type %q, want brevis (the custom node)", n.ID, n.Type)
		}
	}
	if x["a"] >= x["b"] || x["b"] >= x["d"] {
		t.Errorf("colunas fora de ordem: a=%d b=%d d=%d", x["a"], x["b"], x["d"])
	}
	if x["b"] != x["c"] {
		t.Errorf("b and c run in parallel but came out in different columns: %d and %d", x["b"], x["c"])
	}
	if y["b"] == y["c"] {
		t.Errorf("b e c sairam sobrepostos em y=%d", y["b"])
	}
	if y["a"] != y["d"] {
		t.Errorf("single-node levels should stay centred: a=%d d=%d", y["a"], y["d"])
	}
}

// With no run, every node is "pending" -- the screen of a workflow that never ran
// must not inherit state from anywhere.
func TestTheWorkflowGraphHasNoState(t *testing.T) {
	ui := newUI(defsFake{w: diamond()}, execsFake{})
	_, g := request(t, ui, "/api/workflows/diamond/graph")

	for _, n := range g.Nodes {
		if n.Data["status"] != "pending" {
			t.Errorf("node %s has status %v, want pending", n.ID, n.Data["status"])
		}
	}
	if g.RunID != "" {
		t.Errorf("run_id = %q on a graph with no run", g.RunID)
	}
	if g.Nodes[0].Data["acao"] == nil {
		t.Error("o card perdeu o rotulo da acao")
	}
}

func TestTheRunGraphAppliesStatePerNode(t *testing.T) {
	id := uuid.New()
	def, _ := json.Marshal(diamond())
	output := 2
	ui := newUI(defsFake{err: errors.New("it must not query the published definition")}, execsFake{
		run: dom.Run{ID: id, WorkflowSlug: "diamond", Status: dom.StatusRunning, Definition: def},
		states: map[string]postgres.NodeState{
			"a": {NodeID: "a", Status: "success", DurationMs: 1200},
			"b": {NodeID: "b", Status: "running"},
			"c": {NodeID: "c", Status: "failed", Attempt: 2, ExitCode: &output, Err: "boom"},
		},
	})

	res, g := request(t, ui, "/api/runs/"+id.String()+"/graph")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if g.RunID != id.String() || g.Status != "running" {
		t.Fatalf("cabecalho errado: run=%q status=%q", g.RunID, g.Status)
	}
	if g.Terminal {
		t.Error("a running run marked terminal: the UI would stop refreshing")
	}

	porID := map[string]map[string]any{}
	for _, n := range g.Nodes {
		porID[n.ID] = n.Data
	}
	if porID["a"]["status"] != "success" || porID["a"]["duracao_ms"] != float64(1200) {
		t.Errorf("no a: %v", porID["a"])
	}
	if porID["c"]["erro"] != "boom" || porID["c"]["exit_code"] != float64(2) {
		t.Errorf("node c lost its error/exit code: %v", porID["c"])
	}
	// d never ran; it has to stay grey rather than inherit the parent's state.
	if porID["d"]["status"] != "pending" {
		t.Errorf("no d: %v", porID["d"])
	}

	for _, e := range g.Edges {
		if e.Target == "b" && !e.Animated {
			t.Error("the edge reaching the running node should be animated")
		}
		if e.Target == "d" && e.Animated {
			t.Error("an edge into an idle node must not animate")
		}
	}
}

// A terminal graph has to say so in the JSON: it is the signal that makes the
// island stop asking. Without it, every finished run leaves an eternal poll every
// 2s.
func TestTheRunGraphMarksTerminal(t *testing.T) {
	id := uuid.New()
	def, _ := json.Marshal(diamond())
	ui := newUI(defsFake{}, execsFake{
		run: dom.Run{ID: id, Status: dom.StatusSuccess, Definition: def},
	})
	_, g := request(t, ui, "/api/runs/"+id.String()+"/graph")
	if !g.Terminal {
		t.Error("a run at success was not marked terminal")
	}
}

func TestTheGraphRefusesInvalidInput(t *testing.T) {
	casos := []struct {
		name     string
		ui       *api.UI
		path     string
		expected int
	}{
		{"workflow inexistente", newUI(defsFake{err: errors.New("no rows")}, execsFake{}),
			"/api/workflows/fantasma/graph", http.StatusNotFound},
		{"uuid malformado", newUI(defsFake{}, execsFake{}),
			"/api/runs/nao-e-uuid/graph", http.StatusBadRequest},
		{"run inexistente", newUI(defsFake{}, execsFake{err: errors.New("no rows")}),
			"/api/runs/" + uuid.New().String() + "/graph", http.StatusNotFound},
		{"ciclo gravado no testDB", newUI(defsFake{w: wf.Workflow{
			Slug:  "ciclo",
			Nodes: []wf.Node{{ID: "a", Run: "x"}, {ID: "b", Run: "y"}},
			Edges: []wf.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
		}}, execsFake{}), "/api/workflows/ciclo/graph", http.StatusUnprocessableEntity},
	}
	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			res, _ := request(t, c.ui, c.path)
			if res.StatusCode != c.expected {
				t.Errorf("status = %d, want %d", res.StatusCode, c.expected)
			}
		})
	}
}

// the stages of an SDK step, the way the runner's collector writes them.
func fourStages() []postgres.Stage {
	ms := int64(2400)
	return []postgres.Stage{
		{Name: "check", State: "done"},
		{Name: "extract", State: "done", Ms: &ms, Numbers: map[string]any{"paginas": 300}},
		{Name: "transform", State: "done", Numbers: map[string]any{"pulados": 13}},
		{Name: "load", State: "running"},
	}
}

func graphWithStages(t *testing.T, states map[string]postgres.NodeState) graph {
	t.Helper()
	id := uuid.New()
	def, _ := json.Marshal(diamond())
	ui := newUI(defsFake{}, execsFake{
		run:    dom.Run{ID: id, WorkflowSlug: "diamond", Status: dom.StatusRunning, Definition: def},
		states: states,
	})
	res, g := request(t, ui, "/api/runs/"+id.String()+"/graph")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	return g
}

// An SDK step becomes a GROUP, with the stages inside it. The DAG's edges stay
// between steps, so the group takes a single column -- and "same column
// significa runStep em paralelo" continua verdade.
func TestAnSDKStepBecomesAGroupWithTheStagesInside(t *testing.T) {
	g := graphWithStages(t, map[string]postgres.NodeState{
		"b": {NodeID: "b", Status: "running", Stages: fourStages(), SdkVersion: "v0.44.1"},
	})

	pai := -1
	var children []int
	for i, n := range g.Nodes {
		if n.ID == "b" {
			pai = i
		}
		if n.ParentID == "b" {
			children = append(children, i)
		}
	}
	if pai < 0 {
		t.Fatal("the step vanished from the graph")
	}
	if len(children) != 4 {
		t.Fatalf("it came out with %d stages, expected 4", len(children))
	}

	// React Flow requires the parent BEFORE the children in the array.
	for _, f := range children {
		if f < pai {
			t.Fatal("a child came out before the parent; React Flow does not build the group")
		}
	}

	height, ok := g.Nodes[pai].Style["height"].(float64)
	if !ok || height <= 0 {
		t.Fatalf("the group came out with no declared height: %v", g.Nodes[pai].Style)
	}
	for _, f := range children {
		n := g.Nodes[f]
		if n.Type != "etapa" || n.Extent != "parent" {
			t.Errorf("filho mal formado: %+v", n)
		}
		if n.Selectable == nil || *n.Selectable {
			t.Errorf("a selectable stage would open an empty panel: %+v", n)
		}
		if float64(n.Position.Y)+26 > height {
			t.Errorf("stage %s spills out of the group: y=%d, height=%v", n.ID, n.Position.Y, height)
		}
	}
}

// The real cost of nesting: the column was centred assuming a FIXED height, and
// an expanded group ran over its neighbour.
//
// Each node's height is measured by what it DRAWS -- the last stage inside it --
// and not by the height it declares. Checking against the declared one would
// be
// conferir o layout consigo mesmo: quem errasse as duas juntas passaria.
func TestAColumnDoesNotOverlapWithAnExpandedNode(t *testing.T) {
	g := graphWithStages(t, map[string]postgres.NodeState{
		"b": {NodeID: "b", Status: "running", Stages: fourStages(), SdkVersion: "v0.44.1"},
		"c": {NodeID: "c", Status: "pending"},
	})

	// The bottom of each node, measured by the children it carries.
	fundo := map[string]int{}
	for _, n := range g.Nodes {
		if n.ParentID == "" {
			continue
		}
		if b := n.Position.Y + 26; b > fundo[n.ParentID] {
			fundo[n.ParentID] = b
		}
	}

	height := func(id string) int {
		if f := fundo[id]; f > 0 {
			return f + 10 // a folga de rodape do grupo
		}
		return 84 // a card with no stages
	}

	var b, c int
	temB, temC := false, false
	for _, n := range g.Nodes {
		switch {
		case n.ID == "b" && n.ParentID == "":
			b, temB = n.Position.Y, true
		case n.ID == "c" && n.ParentID == "":
			c, temC = n.Position.Y, true
		}
	}
	if !temB || !temC {
		t.Fatal("b e c precisam estar no grafo")
	}

	// b e c estao no MESMO nivel do diamond: um tem de acabar antes de o
	// outro comecar.
	top, bottom, topHeight := b, c, height("b")
	if c < b {
		top, bottom, topHeight = c, b, height("c")
	}
	if top+topHeight > bottom {
		t.Errorf("os nos se sobrepoem: um vai de %d a %d, o outro comeca em %d",
			top, top+topHeight, bottom)
	}
}

// The badge says the step was built with the SDK, and with which version.
func TestTheSDKBadgeComesOutOnTheNode(t *testing.T) {
	g := graphWithStages(t, map[string]postgres.NodeState{
		"b": {NodeID: "b", Status: "running", Stages: fourStages(), SdkVersion: "v0.44.1"},
		"c": {NodeID: "c", Status: "success"},
	})
	for _, n := range g.Nodes {
		switch n.ID {
		case "b":
			if n.Data["sdk"] != "v0.44.1" {
				t.Errorf("the SDK step came out with no badge: %v", n.Data)
			}
		case "c":
			if _, tem := n.Data["sdk"]; tem {
				t.Errorf("a step that announced nothing got a badge: %v", n.Data)
			}
		}
	}
}

// A step that is not an SDK one stays exactly as it was: no group, no children,
// no new field. A missing stage must never change the screen of a step that
// works.
func TestAPlainStepGainsNoNewField(t *testing.T) {
	g := graphWithStages(t, map[string]postgres.NodeState{
		"a": {NodeID: "a", Status: "success", DurationMs: 1200},
	})
	for _, n := range g.Nodes {
		if n.ParentID != "" || n.Extent != "" || n.Style != nil || n.Selectable != nil {
			t.Errorf("a plain node gained a nesting field: %+v", n)
		}
		if n.Type != "brevis" {
			t.Errorf("no comum mudou de tipo: %q", n.Type)
		}
	}
}

// The chip's data reaches the payload, with its source.
func TestTheNodeCarriesWhatItRunsIn(t *testing.T) {
	def := wf.Workflow{
		Slug: "runs-in", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "extract", Run: "python fetch.py"},
			{ID: "transform", Run: "dbt build", Image: "python:3.12"},
			{ID: "declared", Run: "/opt/wrapper.sh", Runtime: "go"},
			{ID: "opaque", Run: "/opt/brevis/bin/fetch-weather"},
		},
	}

	ui := newUI(defsFake{w: def}, execsFake{})
	_, g := request(t, ui, "/api/workflows/runs-in/graph")

	byID := map[string]map[string]any{}
	for _, n := range g.Nodes {
		byID[n.ID] = n.Data
	}

	if got := byID["extract"]["runtime"]; got != "python" {
		t.Errorf("extract runtime = %v, want python", got)
	}
	if got := byID["extract"]["runtime_source"]; got != "inferred" {
		t.Errorf("extract source = %v, want inferred", got)
	}

	// The command wins over the image: an image named python running dbt is a
	// dbt step.
	if got := byID["transform"]["runtime"]; got != "python" {
		t.Errorf("transform runtime = %v", got)
	}
	if tools, _ := byID["transform"]["tools"].([]any); len(tools) != 1 || tools[0] != "dbt" {
		t.Errorf("transform tools = %v, want [dbt]", byID["transform"]["tools"])
	}

	// Declared beats the parser, and the source says so -- which is what makes
	// the two drawable differently.
	if got := byID["declared"]["runtime"]; got != "go" {
		t.Errorf("declared runtime = %v, want go", got)
	}
	if got := byID["declared"]["runtime_source"]; got != "declared" {
		t.Errorf("declared source = %v, want declared", got)
	}

	// A step the engine cannot read carries NOTHING. Not an empty string, not
	// a source: the keys are absent, so the payload is what it was before this
	// feature and an older dag.js draws the same card.
	for _, key := range []string{"runtime", "tools", "runtime_source"} {
		if _, present := byID["opaque"][key]; present {
			t.Errorf("an unreadable step carries %q: %v", key, byID["opaque"][key])
		}
	}
}

// What a step published reaches the graph, and a step that published nothing
// changes nothing.
//
// The second half is the one that needs asserting: most steps publish nothing,
// and their card has to be byte for byte what it was before this feature -- no
// empty key, no "0 published".
func TestTheNodeCarriesWhatTheStepPublished(t *testing.T) {
	def := wf.Workflow{
		Slug: "ctx", Kind: wf.KindDAG,
		Nodes: []wf.Node{{ID: "extract", Run: "x"}, {ID: "quiet", Run: "y"}},
		Edges: []wf.Edge{{From: "extract", To: "quiet"}},
	}
	states := map[string]postgres.NodeState{
		"extract": {
			NodeID: "extract", Status: "success",
			Published: json.RawMessage(`{"bucket":"s3://landing","rows":48213}`),
		},
		"quiet": {NodeID: "quiet", Status: "success"},
	}

	id := uuid.New()
	raw, _ := json.Marshal(def)
	ui := newUI(defsFake{}, execsFake{
		run:    dom.Run{ID: id, WorkflowSlug: "ctx", Status: dom.StatusSuccess, Definition: raw},
		states: states,
	})
	_, g := request(t, ui, "/api/runs/"+id.String()+"/graph")

	byID := map[string]map[string]any{}
	for _, n := range g.Nodes {
		byID[n.ID] = n.Data
	}

	ctx, ok := byID["extract"]["context"].(map[string]any)
	if !ok {
		t.Fatalf("extract carries no context: %v", byID["extract"])
	}
	if ctx["bucket"] != "s3://landing" {
		t.Errorf("context = %v", ctx)
	}
	// The number stays a number: a step reading 48213 through the SDK and
	// seeing 48213.0 on the screen is the class of difference this project has
	// already paid for once.
	if ctx["rows"] != float64(48213) {
		t.Errorf("rows = %#v, want the number as written", ctx["rows"])
	}

	if _, present := byID["quiet"]["context"]; present {
		t.Errorf("a step that published nothing carries a context key: %v", byID["quiet"])
	}

	// And what the runner HANDED the step below, keyed by who wrote it. That
	// keying is the provenance the panel shows -- without it the screen would
	// say a value exists without saying where it came from, which on a wide DAG
	// is the whole question.
	avail, ok := byID["quiet"]["available"].(map[string]any)
	if !ok {
		t.Fatalf("quiet was handed nothing, though it depends on extract: %v", byID["quiet"])
	}
	from, ok := avail["extract"].(map[string]any)
	if !ok || from["bucket"] != "s3://landing" {
		t.Errorf("available = %v; want extract's values under extract's name", avail)
	}

	// The FIRST step was handed nothing, and carries no key at all. A step with
	// no dependencies has no context available, and an empty object on its card
	// would say it ran with an empty input rather than with none.
	if _, present := byID["extract"]["available"]; present {
		t.Errorf("a step with no dependencies carries an available key: %v",
			byID["extract"])
	}
}
