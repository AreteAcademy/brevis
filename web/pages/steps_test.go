package pages

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// The run in the report: discover publishes three partitions, load maps over
// them, and the logs arrive in the order the instances STARTED -- 2, 1, 0 --
// with `end` started before the step that maps after it.
func reportedRun(t *testing.T) (run.Run, []postgres.StepLog) {
	t.Helper()
	def := map[string]any{
		"Slug": "daily_sales",
		"Nodes": []map[string]any{
			{"ID": "start"},
			{"ID": "discover"},
			{"ID": "load", "ForEach": "discover.partitions"},
			{"ID": "end"},
		},
		"Edges": []map[string]any{
			{"From": "start", "To": "discover"},
			{"From": "discover", "To": "load"},
			{"From": "load", "To": "end"},
		},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	logs := []postgres.StepLog{
		{NodeID: "start", MapIndex: run.Unmapped, Status: "success"},
		{NodeID: "end", MapIndex: run.Unmapped, Status: "success"},
		{NodeID: "discover", MapIndex: run.Unmapped, Status: "success",
			Output: json.RawMessage(`{"partitions": ["2026-09-05", "2026-09-06", "2026-09-07"], "source_dir": "/data/incoming"}`)},
		{NodeID: "load", MapIndex: 2, Status: "success"},
		{NodeID: "load", MapIndex: 1, Status: "failed"},
		{NodeID: "load", MapIndex: 0, Status: "success"},
	}
	return run.Run{Definition: raw}, logs
}

// The section reads in the graph's order, as the flow above it does -- not
// in the order the steps happened to start.
func TestStepOutputFollowsTheGraph(t *testing.T) {
	r, logs := reportedRun(t)
	var got []string
	for _, g := range StepOutputs(r, logs) {
		got = append(got, g.Node)
	}
	want := []string{"start", "discover", "load", "end"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// A mapped step is ONE entry, as it is one node in the flow, with its count
// and each instance named by the item it ran for.
func TestAMappedStepIsOneEntryWithItsInstancesNamed(t *testing.T) {
	r, logs := reportedRun(t)
	load := StepOutputs(r, logs)[2]
	if !load.Mapped || load.Total != 3 || load.Ok != 2 {
		t.Fatalf("load = mapped %v, %d of %d ok; want mapped, 2 of 3", load.Mapped, load.Ok, load.Total)
	}
	if load.Status != "failed" {
		t.Errorf("load's status = %q, want failed: one instance failed", load.Status)
	}
	var items []string
	for _, row := range load.Rows {
		items = append(items, row.Item)
	}
	want := []string{"2026-09-05", "2026-09-06", "2026-09-07"}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %v, want %v", items, want)
	}
}

// What a step published is on the screen, as it is on its node in the flow.
func TestAStepsOutputIsShown(t *testing.T) {
	r, logs := reportedRun(t)
	discover := StepOutputs(r, logs)[1]
	want := []OutputLine{
		{Key: "partitions", Value: `["2026-09-05","2026-09-06","2026-09-07"]`},
		{Key: "source_dir", Value: "/data/incoming"},
	}
	if !reflect.DeepEqual(discover.Rows[0].Output, want) {
		t.Errorf("output = %#v, want %#v", discover.Rows[0].Output, want)
	}
}

// A definition the page cannot read still shows every log, in the order they
// came: the section is the place somebody looks when everything else broke.
func TestAnUnreadableDefinitionStillShowsEveryLog(t *testing.T) {
	_, logs := reportedRun(t)
	n := 0
	for _, g := range StepOutputs(run.Run{Definition: []byte("not json")}, logs) {
		n += len(g.Rows)
	}
	if n != len(logs) {
		t.Errorf("%d rows shown, want %d", n, len(logs))
	}
}

// A mapped step is green only when every instance is: any other status beside
// a success is what the node shows -- one the ranking already knows, or one
// added after it.
func TestOnlyAllSuccessIsSuccess(t *testing.T) {
	for _, other := range []string{"pending", "created", "queued", "running",
		"retrying", "failed", "canceled", "skipped", "waiting"} {
		got := worst(map[int]string{0: string(run.StatusSuccess), 1: other})
		if got != other {
			t.Errorf("success beside %s shows %s", other, got)
		}
	}
	if got := worst(map[int]string{0: "success", 1: "success"}); got != "success" {
		t.Errorf("all success shows %s", got)
	}
}
