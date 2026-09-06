package sdk

import (
	"context"
	"strings"
	"testing"
)

func csvRows() []any {
	return []any{
		map[string]any{"area": "north", "year": "2026", "n": "10", "id": "a"},
		map[string]any{"area": "north", "year": "2026", "n": "5", "id": "b"},
		map[string]any{"area": "south", "year": "2026", "n": "7", "id": "c"},
	}
}

// The case that asked for this: identity is derived from the record that
// LANDS, and that record does not exist before the aggregation.
//
// Before Stages the only way out was to build the id inside Finish -- which
// meant reimplementing IngestionID by hand. A feature that forces you to work
// around another feature is not finished.
func TestIdentityIsComputedAfterTheAggregation(t *testing.T) {
	var box []Envelope
	log, err := rodar(t, &Pipeline{
		Name:   "fetcher",
		Source: Source{From: origemContada{registros: csvRows(), leituras: new(int)}},
		Stages: []Stage{
			Aggregate(Reduce{
				By:  GroupBy("area", "year"),
				Agg: map[string]Aggregator{"total": Sum("n")},
			}),
			Map(
				Compute("provider", func(map[string]any) (any, error) { return "p", nil }),
				Compute("entity", func(map[string]any) (any, error) { return "e", nil }),
				Compute("source_key", func(r map[string]any) (any, error) { return Key("area", "year")(r) }),
				Compute("record_ts", func(map[string]any) (any, error) { return "2026-01-01T00:00:00Z", nil }),
				IngestionID(),
			),
		},
		Target: Target{To: destinoQueGuarda{recebido: &box}},
		Run:    RunContext{ID: "run-stages"},
	})
	if err != nil {
		t.Fatalf("pipeline: %v\n%s", err, log)
	}

	if len(box) != 2 {
		t.Fatalf("loaded %d rows, want 2 groups", len(box))
	}
	for _, env := range box {
		row := env.Payload.(map[string]any)
		if row["ingestion_id"] == nil || row["ingestion_id"] == "" {
			t.Fatalf("the aggregated row has no identity: %v", row)
		}
		if row["total"] == nil {
			t.Fatalf("the aggregation was lost: %v", row)
		}
	}
	// And the two rows must differ, or the key describes nothing.
	a := box[0].Payload.(map[string]any)["ingestion_id"]
	b := box[1].Payload.(map[string]any)["ingestion_id"]
	if a == b {
		t.Errorf("both groups got the same id: %v", a)
	}
}

// An aggregation that receives records already carrying identity does not
// raise on its own -- it aggregates the id and produces a key that corresponds
// to nothing. Which is worse.
func TestAggregateRefusesRecordsThatAlreadyHaveIdentity(t *testing.T) {
	var box []Envelope
	_, err := rodar(t, &Pipeline{
		Name:   "fetcher",
		Source: Source{From: origemContada{registros: csvRows(), leituras: new(int)}},
		Stages: []Stage{
			Map(
				Compute("provider", func(map[string]any) (any, error) { return "p", nil }),
				Compute("entity", func(map[string]any) (any, error) { return "e", nil }),
				Compute("source_key", func(r map[string]any) (any, error) { return Key("id")(r) }),
				Compute("record_ts", func(map[string]any) (any, error) { return "2026-01-01T00:00:00Z", nil }),
				IngestionID(),
			),
			Aggregate(Reduce{By: GroupBy("area"), Agg: map[string]Aggregator{"n": Count()}}),
		},
		Target: Target{To: destinoQueGuarda{recebido: &box}},
		Run:    RunContext{ID: "run-guard"},
	})
	if err == nil {
		t.Fatal("aggregating over records that carry identity was allowed")
	}
	for _, want := range []string{"ingestion_id", "corresponds to nothing", "after the last"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not say %q: %v", want, err)
		}
	}
}

// Two descriptions of the same thing, and one would lose in silence.
func TestStagesTogetherWithTransformIsAnError(t *testing.T) {
	for name, p := range map[string]*Pipeline{
		"with Transform": {Transform: []Transformer{Without("x")}, Stages: []Stage{Map()}},
		"with Reduce":    {Reduce: &Reduce{Agg: map[string]Aggregator{"n": Count()}}, Stages: []Stage{Map()}},
	} {
		_, err := p.stages()
		if err == nil {
			t.Errorf("%s: passed", name)
			continue
		}
		if !strings.Contains(err.Error(), "ignored in silence") {
			t.Errorf("%s: the message does not say what is at stake: %v", name, err)
		}
	}
}

// Transform and Reduce stay as shorthand, and desugar into the same list.
func TestTransformAndReduceDesugarIntoStages(t *testing.T) {
	p := &Pipeline{
		Transform: []Transformer{Without("id")},
		Reduce:    &Reduce{By: GroupBy("area"), Agg: map[string]Aggregator{"n": Count()}},
	}
	stages, err := p.stages()
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 || stages[0].kind != StageMap || stages[1].kind != StageAggregate {
		t.Fatalf("desugared into %d stages: %+v", len(stages), stages)
	}
}

// "5.515 linhas" não diz nada sobre onde foram as outras seis milhões.
func TestResultCountsEachStage(t *testing.T) {
	var box []Envelope
	p := &Pipeline{
		Name:   "fetcher",
		Source: Source{From: origemContada{registros: csvRows(), leituras: new(int)}},
		Stages: []Stage{
			Map(SkipWithout("id")),
			Aggregate(Reduce{By: GroupBy("area"), Agg: map[string]Aggregator{"n": Count()}}),
		},
		Target: Target{To: destinoQueGuarda{recebido: &box}},
		Run:    RunContext{ID: "run-counts"},
	}

	stages, err := p.stages()
	if err != nil {
		t.Fatal(err)
	}
	contagens := make([]StageResult, len(stages))
	data, err := Extract(context.Background(), p.Source)
	if err != nil {
		t.Fatal(err)
	}
	for i, st := range stages {
		data.Records = st.apply(data.Records, &contagens[i], "teste")
	}
	if _, err := loadWith(context.Background(), data, p.Target, p.Run); err != nil {
		t.Fatal(err)
	}

	if contagens[0].Kind != StageMap || contagens[0].In != 3 || contagens[0].Out != 3 {
		t.Errorf("map stage: %+v", contagens[0])
	}
	if contagens[1].Kind != StageAggregate || contagens[1].In != 3 || contagens[1].Out != 2 {
		t.Errorf("aggregate stage: %+v", contagens[1])
	}
	if contagens[1].Groups != 2 {
		t.Errorf("groups = %d, want 2", contagens[1].Groups)
	}
}
