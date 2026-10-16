package api_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func ms(v int64) *int64 { return &v }

// A ONE-STEP RUN SHOWS ITS STEP, NOT A CANVAS (screen-run-v2 RV3): no graph
// island, and in its place the step's line with each SDK stage the graph
// would have drawn -- the same labels and numbers, from the same build --
// inside the region the live refresh swaps.
func TestAOneStepRunShowsItsStepNotACanvas(t *testing.T) {
	one := wf.Workflow{Slug: "single", Nodes: []wf.Node{
		{ID: "start", Marker: true},
		{ID: "load", Run: "/opt/brevis/bin/pipeline load", Runtime: "go"},
	}, Edges: []wf.Edge{{From: "start", To: "load"}}}
	def, _ := json.Marshal(one)
	id := uuid.New()
	e := execsFake{
		run: dom.Run{ID: id, WorkflowSlug: "single", Status: dom.StatusSuccess, Definition: def},
		states: map[string]postgres.NodeState{"load": {Status: "success", DurationMs: 31, SdkVersion: "v0.84.1",
			Published: json.RawMessage(`{"rows":3}`),
			Stages: []postgres.Stage{
				{Index: 0, Name: "check", State: "done", Ms: ms(0)},
				{Index: 1, Name: "extract", State: "done", Ms: ms(1), Numbers: map[string]any{"pages": 1.0, "bytes": 61.0, "detail": "/data/incoming/2026-09-05.csv"}},
				{Index: 2, Name: "map", State: "done", Numbers: map[string]any{"in": 3.0, "out": 3.0}},
				{Index: 3, Name: "load", State: "done", Ms: ms(1), Numbers: map[string]any{"rows": 3.0, "objects": 1.0}},
			}}},
	}
	ui := newUIWithReads(defsFake{w: one}, e)
	_, page := get(t, ui, "/runs/"+id.String())

	if strings.Contains(page, `id="dag"`) || strings.Contains(page, "dag.js") {
		t.Error("a one-step run still mounts the graph island")
	}
	at := strings.Index(page, "data-step-line")
	if at < 0 {
		t.Fatal("no step line")
	}
	line := page[at:]
	text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(line, " ")), " ")
	for _, want := range []string{"load", "/opt/brevis/bin/pipeline load", "SDK v0.84.1", "Go",
		"check", "source", "map", "target", "3 in", "3 out", "1 pages", "3 rows", "1 objects",
		"/data/incoming/2026-09-05.csv", "1 value published"} {
		if !strings.Contains(text, want) {
			t.Errorf("the step line lost %q", want)
		}
	}
	if strings.Contains(text, "extract") || strings.Contains(text, "61 bytes") {
		t.Error("the step line says the wire's names, or a number the graph does not show")
	}

	_, live := get(t, ui, "/runs/"+id.String()+"/live")
	if !strings.Contains(live, "data-step-line") {
		t.Error("the live refresh does not carry the step line")
	}
}
