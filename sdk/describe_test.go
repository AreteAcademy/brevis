package sdk

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// upperName is a transformer with a name of its own.
func upperName(p any) (any, error) { return p, nil }

// inlineTransformer is a function literal with no function around it.
var inlineTransformer Transformer = func(p any) (any, error) { return p, nil }

// configuredTarget is a writer that says how it is set up.
type configuredTarget struct {
	keepingTarget
	settings []Setting
}

func (c configuredTarget) Configuration() []Setting { return c.settings }

// pipelineEvent is the `pipeline` marker a run printed, or nil.
func pipelineEvent(t *testing.T, events []map[string]any) map[string]any {
	t.Helper()
	for _, ev := range events {
		if ev["type"] == "pipeline" {
			return ev
		}
	}
	return nil
}

// fieldsOf is the marker's settings, by path.
func fieldsOf(t *testing.T, ev map[string]any) (map[string]any, []string) {
	t.Helper()
	// What the engine reads is the JSON, so the test reads the JSON too.
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	list, ok := ev["fields"].([]any)
	if !ok {
		t.Fatalf("the marker has no fields: %v", ev)
	}
	byPath := map[string]any{}
	var order []string
	for _, f := range list {
		m := f.(map[string]any)
		byPath[m["path"].(string)] = m["value"]
		order = append(order, m["path"].(string))
	}
	return byPath, order
}

// The pipeline says how it is configured once, before the first phase: a run
// that fails at the source still says what it tried to do.
func TestThePipelineDescribesItselfBeforeItsFirstPhase(t *testing.T) {
	var box []Envelope
	events, err := stagesOf(t, pipelineDeTeste(countedSource{
		records: []any{map[string]any{"id": 1}}, leituras: new(int)}, &box))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 || events[0]["type"] != "sdk" || events[1]["type"] != "pipeline" {
		t.Fatalf("events = %v, want the pipeline's description right after the announcement", events)
	}
	if v := events[1]["v"]; v != float64(1) {
		t.Fatalf("v = %v", v)
	}
	f, order := fieldsOf(t, events[1])
	if f["source.driver"] != "sdk.countedSource" || f["target.driver"] != "sdk.keepingTarget" {
		t.Fatalf("drivers = %v", f)
	}
	if !reflect.DeepEqual(f["transform.1.map"], []any{"sdk.SkipWithout"}) {
		t.Fatalf("transform.1.map = %#v", f["transform.1.map"])
	}
	// Source, then the stages, then the target: the order it runs in.
	if order[0] != "source.driver" || !strings.HasPrefix(order[len(order)-1], "target.") {
		t.Fatalf("order = %v", order)
	}
}

// A transformer is named by its function. One made by a factory is named by
// the factory; one written inline in place has no name worth showing.
func TestATransformerIsNamedByItsFunction(t *testing.T) {
	cases := map[string]Transformer{
		"sdk.upperName":      upperName,
		"sdk.SkipWithout":    SkipWithout("id"),
		"anonymous function": inlineTransformer,
	}
	for want, fn := range cases {
		if got := transformerName(fn); got != want {
			t.Errorf("transformerName = %q, want %q", got, want)
		}
	}
}

// An aggregation is declarative, so it is described whole: what it groups
// by, and what each column computes from which field.
func TestAnAggregationIsDescribedWhole(t *testing.T) {
	p := &Pipeline{
		Source: Source{From: slowSource{}},
		Stages: []Stage{
			Map(upperName),
			Aggregate(Reduce{By: GroupBy("area", "year"), Agg: map[string]Aggregator{
				"total": Sum("valor"), "n": Count(), "top": MaxBy("valor", "id")}}),
		},
		Target: Target{To: keepingTarget{received: new([]Envelope)}},
	}
	f, _ := fieldsOf(t, p.describe())
	if !reflect.DeepEqual(f["transform.2.aggregate.by"], []any{"area", "year"}) {
		t.Fatalf("by = %#v", f["transform.2.aggregate.by"])
	}
	for path, want := range map[string]string{
		"transform.2.aggregate.n":     "count",
		"transform.2.aggregate.total": "sum(valor)",
		"transform.2.aggregate.top":   "max_by(valor, id)",
	} {
		if f[path] != want {
			t.Errorf("%s = %v, want %q", path, f[path], want)
		}
	}
}

// The Target's own declaration, and whatever the writer says about itself.
func TestTheTargetCarriesItsDeclarationAndTheWritersSettings(t *testing.T) {
	p := &Pipeline{
		Source: Source{From: slowSource{}},
		Target: Target{
			To: configuredTarget{settings: []Setting{
				{Key: "table", Value: "landing.orders"}, {Key: "create_table", Value: true}}},
			Columns:     []string{"id", "total"},
			PartitionBy: "loaded_at",
			Dedup:       "merge",
		},
	}
	f, _ := fieldsOf(t, p.describe())
	want := map[string]any{
		"target.driver":       "sdk.configuredTarget",
		"target.table":        "landing.orders",
		"target.create_table": true,
		"target.columns":      []any{"id", "total"},
		"target.partition_by": "loaded_at",
		"target.dedup":        "merge",
	}
	for path, v := range want {
		if !reflect.DeepEqual(f[path], v) {
			t.Errorf("%s = %#v, want %#v", path, f[path], v)
		}
	}
}

// A description too large for one line is not sent: the executors break the
// log on lines, and half a description would be refused anyway. The run
// itself goes on, and says so.
func TestADescriptionTooLargeIsNotSent(t *testing.T) {
	p := pipelineDeTeste(countedSource{records: []any{map[string]any{"id": 1}}, leituras: new(int)}, new([]Envelope))
	p.Target.To = configuredTarget{
		keepingTarget: keepingTarget{received: new([]Envelope)},
		settings:      []Setting{{Key: "huge", Value: strings.Repeat("x", describeCeiling)}}}
	events, err := stagesOf(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if ev := pipelineEvent(t, events); ev != nil {
		t.Fatalf("an oversized description was sent: %d bytes", len(ev["fields"].([]any)))
	}
	if events[len(events)-1]["name"] != "load" {
		t.Fatal("the run did not go on without its description")
	}
}

// By hand, nothing is printed: the lines serve nobody outside the engine.
func TestByHandThePipelineDoesNotDescribeItself(t *testing.T) {
	p := pipelineDeTeste(countedSource{records: []any{map[string]any{"id": 1}}, leituras: new(int)}, new([]Envelope))
	p.Run = RunContext{}
	events, _ := stagesOf(t, p)
	if pipelineEvent(t, events) != nil {
		t.Fatal("described outside the engine")
	}
	_ = context.Background
}
