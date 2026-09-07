package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// capturarSaida runs f with stdout redirected, and returns what it printed.
func captureOutput(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w

	failure := f()

	os.Stdout = previous
	_ = w.Close()
	output, _ := io.ReadAll(r)
	return string(output), failure
}

// pipelineDeEstagios is the shape the defect report described: a source whose
// rows are reduced by an aggregation, with identity computed afterwards.
func pipelineDeEstagios(box *[]Envelope) *Pipeline {
	rows := []any{
		map[string]any{"area": "north", "year": "2026", "n": "10"},
		map[string]any{"area": "north", "year": "2026", "n": "5"},
		map[string]any{"area": "south", "year": "2026", "n": "7"},
		map[string]any{"area": "south", "year": "2025", "n": "2"},
	}
	return &Pipeline{
		Name:   "fetcher",
		Source: Source{From: countedSource{records: rows, leituras: new(int)}},
		Stages: []Stage{
			Aggregate(Reduce{
				By:  GroupBy("area", "year"),
				Agg: map[string]Aggregator{"total": Sum("n")},
			}),
			Map(
				Compute("provider", func(map[string]any) (any, error) { return "p", nil }),
				Compute("entity", func(map[string]any) (any, error) { return "e", nil }),
				ComputeText("source_key", Key("area", "year")),
				Compute("record_ts", func(map[string]any) (any, error) { return "2026-01-01T00:00:00Z", nil }),
				IngestionID(),
			),
		},
		Target: Target{To: keepingTarget{recebido: box}},
		Run:    RunContext{ID: "run-dry"},
	}
}

// THE test of this fix: the dry-run and the run have to produce the same
// records.
//
// Not "the dry-run applies the stages" -- that is one implementation of the
// property, and the property is what has to hold. The two paths drifted once:
// the dry-run applied `p.Transform`, which is empty whenever Stages is
// declared, so it printed five million raw CSV rows where the run would land
// five thousand aggregated ones. With the same confidence, and no warning.
//
// A -dry-run is what people run INSTEAD of writing. One that answers a
// different question is worse than none.
func TestDryRunProducesExactlyWhatTheRunWouldLand(t *testing.T) {
	var doRun []Envelope
	if _, err := runIt(t, pipelineDeEstagios(&doRun)); err != nil {
		t.Fatalf("run: %v", err)
	}

	var doDry []Envelope
	output, err := captureOutput(t, func() error {
		return runDryRun(context.Background(), pipelineDeEstagios(&doDry), 100)
	})
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, output)
	}

	if len(doRun) == 0 {
		t.Fatal("the run landed nothing; the test is comparing two empties")
	}
	if len(doDry) != 0 {
		t.Fatalf("the dry-run WROTE %d records to the destination", len(doDry))
	}

	// The dry-run does not write, so what it produced is what it printed.
	impressos := printedRecords(t, output)
	if len(impressos) != len(doRun) {
		t.Fatalf("dry-run printed %d records, the run landed %d:\n%s",
			len(impressos), len(doRun), output)
	}
	for i, esperado := range doRun {
		querido, _ := json.Marshal(esperado.Payload)
		obtido, _ := json.Marshal(impressos[i])
		if string(querido) != string(obtido) {
			t.Errorf("record %d differs:\n  run:     %s\n  dry-run: %s", i, querido, obtido)
		}
	}
}

// And the counts per stage have to be there, because "5,515 records" says
// nothing about where the other six million went.
func TestDryRunPrintsWhatEachStageDid(t *testing.T) {
	var box []Envelope
	output, err := captureOutput(t, func() error {
		return runDryRun(context.Background(), pipelineDeEstagios(&box), 100)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, esperado := range []string{"aggregate", "groups", "map"} {
		if !strings.Contains(output, esperado) {
			t.Errorf("the preview does not say %q:\n%s", esperado, output)
		}
	}
	// 4 rows in, 3 groups out.
	if !strings.Contains(output, "4 ->         3") {
		t.Errorf("the aggregation's counts are missing:\n%s", output)
	}
}

// A pipeline the run refuses must not pass the dry-run: the refusal is the
// whole point of checking before writing.
func TestDryRunRefusesWhatTheRunWouldRefuse(t *testing.T) {
	var box []Envelope
	casos := map[string]func(*Pipeline){
		"Stages next to Transform": func(p *Pipeline) {
			p.Transform = []Transformer{Without("x")}
		},
		"an aggregator that cannot exist": func(p *Pipeline) {
			p.Stages = []Stage{Aggregate(Reduce{By: GroupBy("area"), Agg: map[string]Aggregator{"m": Median("n")}})}
		},
	}
	for name, quebrar := range casos {
		p := pipelineDeEstagios(&box)
		quebrar(p)

		_, errRun := runIt(t, p)
		if errRun == nil {
			t.Fatalf("%s: the run accepted it; the test is checking nothing", name)
		}
		_, errDry := captureOutput(t, func() error {
			return runDryRun(context.Background(), p, 5)
		})
		if errDry == nil {
			t.Errorf("%s: the dry-run passed on a pipeline the run refuses", name)
		}
	}
}

// registrosImpressos reads back the records the preview printed.
func printedRecords(t *testing.T, output string) []any {
	t.Helper()
	var out []any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var v any
		if err := json.NewDecoder(bytes.NewReader([]byte(line))).Decode(&v); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// The pattern the report named: a capability ships, and the way to exercise it
// does not.
//
// Records is where the vendor logic lives, and until now the only way to run it
// was to make a real request -- because Response could not be built from
// outside this module.
func TestRecordsLogicCanBeTestedWithoutARequest(t *testing.T) {
	// The guard a real fetcher writes: plenty of APIs answer 200 with an error
	// in the body.
	myRead := func(r Response) ([]any, error) {
		doc, err := r.Object()
		if err != nil {
			return nil, err
		}
		if bad, _ := doc["error"].(bool); bad {
			return nil, Reject("vendor refused: %v", doc["reason"])
		}
		return ParallelArrays("hourly", "time", "temp")(doc)
	}

	if _, err := myRead(NewResponse(200, []byte(`{"error":true,"reason":"quota"}`), false)); err == nil {
		t.Error("a 200 carrying an error passed the guard")
	}

	rows, err := myRead(NewResponse(200,
		[]byte(`{"hourly":{"time":["t1","t2"],"temp":[1,2]},"lat":9}`), false))
	if err != nil {
		t.Fatalf("a good response was refused: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expansion produced %d rows, want 2", len(rows))
	}

	// preserveNumbers is not a detail: with it off, `19` and `19.0` are the same
	// float64, and a key composed from them differs from production's.
	comLiteral := NewResponse(200, []byte(`{"id":19.0}`), true)
	doc, err := comLiteral.Object()
	if err != nil {
		t.Fatal(err)
	}
	if got := asText(doc["id"]); got != "19.0" {
		t.Errorf("preserveNumbers did not reach the decoder: id = %q", got)
	}
}
