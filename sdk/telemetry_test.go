package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"
)

// slowSource delivers records with a pause between them, so the extraction's
// duration is measurable rather than zero.
type slowSource struct {
	records []any
	pausa   time.Duration
}

func (slowSource) Describe() string { return "origem.lenta" }

func (o slowSource) Read(context.Context, ReadOptions) (iter.Seq2[Envelope, error], error) {
	return func(yield func(Envelope, error) bool) {
		for _, r := range o.records {
			time.Sleep(o.pausa)
			if !yield(Envelope{Payload: r}, nil) {
				return
			}
		}
	}, nil
}

// stagesOf runs the pipeline and returns the marked lines it announced.
func stagesOf(t *testing.T, p *Pipeline) ([]map[string]any, error) {
	t.Helper()
	var buf bytes.Buffer
	previous := phaseOutput
	phaseOutput = &buf
	defer func() { phaseOutput = previous }()

	err := runPipeline(context.Background(), p)

	var eventos []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, phaseMarker) {
			continue
		}
		var ev map[string]any
		if e := json.Unmarshal([]byte(strings.TrimPrefix(line, phaseMarker)), &ev); e != nil {
			t.Fatalf("linha marcada ilegivel %q: %v", line, e)
		}
		eventos = append(eventos, ev)
	}
	return eventos, err
}

func pipelineDeTeste(src Reader, box *[]Envelope) *Pipeline {
	return &Pipeline{
		Name:      "fetcher",
		Source:    Source{From: src},
		Transform: []Transformer{SkipWithout("id")},
		Target:    Target{To: keepingTarget{received: box}},
		Run:       RunContext{ID: "run-etapas", Attempt: 0},
	}
}

func TestPhasesFollowThePipelineShape(t *testing.T) {
	var box []Envelope
	eventos, err := stagesOf(t, pipelineDeTeste(countedSource{
		records:  []any{map[string]any{"id": 1}, map[string]any{"id": 2}},
		leituras: new(int),
	}, &box))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	var trail []string
	for _, ev := range eventos {
		if ev["type"] == "sdk" {
			trail = append(trail, "sdk")
			continue
		}
		trail = append(trail, ev["name"].(string)+":"+ev["state"].(string))
	}
	// One phase per element of the pipeline's SHAPE: the source, each stage in
	// order, the destination. A single `transform` box could not tell "216
	// became 9" from "216 became 216 and then 9".
	querido := []string{
		"sdk",
		"check:running", "check:done",
		"extract:running",
		"map:running",
		"extract:done", "map:done",
		"load:running", "load:done",
	}
	if strings.Join(trail, " ") != strings.Join(querido, " ") {
		t.Errorf("trilha:\n  %v\nesperada:\n  %v", trail, querido)
	}
}

// The announcement carries the version, because a badge that only says "SDK" is
// true and useless: the version is what answers "why does this step behave
// differently from its neighbour" without anybody opening the Dockerfile.
func TestTheAnnouncementCarriesTheVersion(t *testing.T) {
	var box []Envelope
	eventos, err := stagesOf(t, pipelineDeTeste(countedSource{
		records: []any{map[string]any{"id": 1}}, leituras: new(int),
	}, &box))
	if err != nil {
		t.Fatal(err)
	}
	if len(eventos) == 0 || eventos[0]["type"] != "sdk" {
		t.Fatalf("o primeiro evento tem de ser o anuncio, veio %v", eventos)
	}
	if v, _ := eventos[0]["version"].(string); v == "" {
		t.Error("o anuncio saiu sem versao")
	}
	if eventos[0]["pipeline"] != "fetcher" {
		t.Errorf("o anuncio nao diz de quem e: %v", eventos[0])
	}
}

// The extraction ends when the STREAM is exhausted, not when Extract returns
// the
// iterador.
//
// The chain is lazy: timing the calls would say "extract: 3ms" on a forty-minute
// extraction, and the screen would lie about precisely the stage
// mais longa.
func TestTheSourceDurationMeasuresTheRealExtraction(t *testing.T) {
	var box []Envelope
	p := pipelineDeTeste(slowSource{
		records: []any{map[string]any{"id": 1}, map[string]any{"id": 2}, map[string]any{"id": 3}},
		pausa:   20 * time.Millisecond,
	}, &box)
	eventos, err := stagesOf(t, p)
	if err != nil {
		t.Fatal(err)
	}

	ms := stageDuration(t, eventos, "extract")
	if ms < 50 {
		t.Errorf("extract durou %vms; a origem gasta 60ms, entao a medicao esta "+
			"cronometrando a chamada em vez do fluxo", ms)
	}
}

// The transform reports no duration. It runs per record, interleaved with the
// read, so any number coming out of there would be the time of something
// else.
func TestAMapStageInventsNoDuration(t *testing.T) {
	var box []Envelope
	eventos, err := stagesOf(t, pipelineDeTeste(slowSource{
		records: []any{map[string]any{"id": 1}}, pausa: 20 * time.Millisecond,
	}, &box))
	if err != nil {
		t.Fatal(err)
	}
	end := findStage(t, eventos, "map", "done")
	if _, tem := end["ms"]; tem {
		t.Errorf("a map stage reported a duration: %v", end)
	}
}

// What only the transform knows: how many went in, how many came out, how many
// were
// pulados.
func TestAMapStageSaysHowManyItDropped(t *testing.T) {
	var box []Envelope
	eventos, err := stagesOf(t, pipelineDeTeste(countedSource{
		records: []any{
			map[string]any{"id": 1},
			map[string]any{"sem_id": true},
			map[string]any{"id": 3},
		},
		leituras: new(int),
	}, &box))
	if err != nil {
		t.Fatal(err)
	}
	end := findStage(t, eventos, "map", "done")
	if end["in"] != 3.0 || end["out"] != 2.0 || end["dropped"] != 1.0 {
		t.Errorf("contagem errada: %v", end)
	}
}

// Outside the engine there is nobody to read the stages, and cluttering the
// terminal of whoever is debugging a fetcher would be cost with no return.
func TestOutsideTheEngineNothingIsAnnounced(t *testing.T) {
	var box []Envelope
	p := pipelineDeTeste(countedSource{
		records: []any{map[string]any{"id": 1}}, leituras: new(int),
	}, &box)
	p.Run = RunContext{} // rodando a mao

	eventos, err := stagesOf(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventos) != 0 {
		t.Errorf("anunciou %d eventos rodando a mao: %v", len(eventos), eventos)
	}
	// E o pipeline continua funcionando igual.
	if len(box) != 1 {
		t.Errorf("carregou %d registros, esperado 1", len(box))
	}
}

// The ceiling exists because the log stream becomes a database write on the
// other side: a pipeline in a loop would take Postgres down through the log's
// path.
func TestThePhaseCap(t *testing.T) {
	var buf bytes.Buffer
	previous := phaseOutput
	phaseOutput = &buf
	defer func() { phaseOutput = previous }()

	r := newReporter(RunContext{ID: "run-1"})
	for i := 0; i < phaseCap*3; i++ {
		r.started(PhaseSource)
	}
	if n := strings.Count(buf.String(), phaseMarker); n != phaseCap {
		t.Errorf("emitiu %d linhas, o teto e %d", n, phaseCap)
	}
}

func findStage(t *testing.T, eventos []map[string]any, name, state string) map[string]any {
	t.Helper()
	for _, ev := range eventos {
		if ev["name"] == name && ev["state"] == state {
			return ev
		}
	}
	t.Fatalf("nao achei a etapa %s:%s em %v", name, state, eventos)
	return nil
}

func stageDuration(t *testing.T, eventos []map[string]any, name string) float64 {
	t.Helper()
	ms, ok := findStage(t, eventos, name, "done")["ms"].(float64)
	if !ok {
		t.Fatalf("a etapa %s nao reportou duracao", name)
	}
	return ms
}

// The screen has to show the pipeline the consumer DECLARED.
//
// Two Map stages share a name, and the phases used to be keyed by name -- so
// three declared stages collapsed into two boxes, and a single `transform` box
// could not tell "216 rows became 9" from "216 became 216 and then 9". The
// structure never reached the display.
func TestEachStageGetsItsOwnBox(t *testing.T) {
	var box []Envelope
	eventos, err := stagesOf(t, &Pipeline{
		Name:   "fetcher",
		Source: Source{From: countedSource{records: twoRecords(), leituras: new(int)}},
		Stages: []Stage{
			Map(SkipWithout("provider")),
			Aggregate(Reduce{By: GroupBy("provider"), Agg: map[string]Aggregator{"n": Count()}}),
			Map(Compute("x", func(map[string]any) (any, error) { return 1, nil })),
		},
		Target: Target{To: keepingTarget{received: &box}},
		Run:    RunContext{ID: "run-boxes"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Every phase that finished, by position.
	porIndice := map[float64]map[string]any{}
	for _, ev := range eventos {
		if ev["type"] == "stage" && ev["state"] == "done" {
			porIndice[ev["index"].(float64)] = ev
		}
	}
	if len(porIndice) != 6 {
		t.Fatalf("%d boxes finished, want 6 (check, source, map, aggregate, map, target): %v",
			len(porIndice), porIndice)
	}

	names := []string{"check", "extract", "map", "aggregate", "map", "load"}
	for i, quero := range names {
		ev := porIndice[float64(i)]
		if ev == nil {
			t.Fatalf("no box at position %d", i)
		}
		if ev["name"] != quero {
			t.Errorf("position %d is %q, want %q", i, ev["name"], quero)
		}
	}

	// The aggregation's numbers belong to the aggregation, not to a sum of
	// everything: 2 records in, 1 group out.
	agg := porIndice[3]
	if agg["in"] != 2.0 || agg["out"] != 1.0 || agg["groups"] != 1.0 {
		t.Errorf("the aggregation's counts are wrong: %v", agg)
	}
	// And the map after it saw the aggregated row, not the raw ones.
	if after := porIndice[4]; after["in"] != 1.0 {
		t.Errorf("the map after the aggregation saw %v records, want 1", after["in"])
	}
}

// The card is half a card without saying WHICH source and WHICH destination.
func TestTheSourceAndTargetSayWhichTheyAre(t *testing.T) {
	var box []Envelope
	eventos, err := stagesOf(t, pipelineDeTeste(countedSource{
		records: twoRecords(), leituras: new(int),
	}, &box))
	if err != nil {
		t.Fatal(err)
	}
	if d := findStage(t, eventos, "extract", "done")["detail"]; d != "origem.teste" {
		t.Errorf("the source does not say which it is: %v", d)
	}
	if d := findStage(t, eventos, "load", "done")["detail"]; d != "destino.teste" {
		t.Errorf("the destination does not say which it is: %v", d)
	}
}
