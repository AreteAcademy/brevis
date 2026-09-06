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

// origemLenta entrega registros com pausa entre eles, para que a duracao da
// extracao seja mensuravel e nao zero.
type origemLenta struct {
	registros []any
	pausa     time.Duration
}

func (origemLenta) Describe() string { return "origem.lenta" }

func (o origemLenta) Read(context.Context, ReadOptions) (iter.Seq2[Envelope, error], error) {
	return func(yield func(Envelope, error) bool) {
		for _, r := range o.registros {
			time.Sleep(o.pausa)
			if !yield(Envelope{Payload: r}, nil) {
				return
			}
		}
	}, nil
}

// etapasDe roda o pipeline e devolve as linhas marcadas que ele anunciou.
func etapasDe(t *testing.T, p *Pipeline) ([]map[string]any, error) {
	t.Helper()
	var buf bytes.Buffer
	anterior := phaseOutput
	phaseOutput = &buf
	defer func() { phaseOutput = anterior }()

	err := runPipeline(context.Background(), p)

	var eventos []map[string]any
	for _, linha := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(linha, phaseMarker) {
			continue
		}
		var ev map[string]any
		if e := json.Unmarshal([]byte(strings.TrimPrefix(linha, phaseMarker)), &ev); e != nil {
			t.Fatalf("linha marcada ilegivel %q: %v", linha, e)
		}
		eventos = append(eventos, ev)
	}
	return eventos, err
}

func pipelineDeTeste(origem Reader, caixa *[]Envelope) *Pipeline {
	return &Pipeline{
		Name:      "fetcher",
		Source:    Source{From: origem},
		Transform: []Transformer{SkipWithout("id")},
		Target:    Target{To: destinoQueGuarda{recebido: caixa}},
		Run:       RunContext{ID: "run-etapas", Attempt: 0},
	}
}

func TestPhasesFollowThePipelineShape(t *testing.T) {
	var caixa []Envelope
	eventos, err := etapasDe(t, pipelineDeTeste(origemContada{
		registros: []any{map[string]any{"id": 1}, map[string]any{"id": 2}},
		leituras:  new(int),
	}, &caixa))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	var trilha []string
	for _, ev := range eventos {
		if ev["type"] == "sdk" {
			trilha = append(trilha, "sdk")
			continue
		}
		trilha = append(trilha, ev["name"].(string)+":"+ev["state"].(string))
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
	if strings.Join(trilha, " ") != strings.Join(querido, " ") {
		t.Errorf("trilha:\n  %v\nesperada:\n  %v", trilha, querido)
	}
}

// O anuncio carrega a versao, porque um selo que so diz "SDK" e verdadeiro e
// inutil: a versao e o que responde "por que este passo se comporta diferente
// do vizinho" sem ninguem abrir o Dockerfile.
func TestTheAnnouncementCarriesTheVersion(t *testing.T) {
	var caixa []Envelope
	eventos, err := etapasDe(t, pipelineDeTeste(origemContada{
		registros: []any{map[string]any{"id": 1}}, leituras: new(int),
	}, &caixa))
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

// A extracao acaba quando o FLUXO se esgota, nao quando Extract devolve o
// iterador.
//
// A cadeia e preguicosa: cronometrar as chamadas diria "extract: 3ms" numa
// extracao de quarenta minutos, e a tela mentiria justamente sobre a etapa
// mais longa.
func TestTheSourceDurationMeasuresTheRealExtraction(t *testing.T) {
	var caixa []Envelope
	p := pipelineDeTeste(origemLenta{
		registros: []any{map[string]any{"id": 1}, map[string]any{"id": 2}, map[string]any{"id": 3}},
		pausa:     20 * time.Millisecond,
	}, &caixa)
	eventos, err := etapasDe(t, p)
	if err != nil {
		t.Fatal(err)
	}

	ms := duracaoDaEtapa(t, eventos, "extract")
	if ms < 50 {
		t.Errorf("extract durou %vms; a origem gasta 60ms, entao a medicao esta "+
			"cronometrando a chamada em vez do fluxo", ms)
	}
}

// O transform nao reporta duracao. Ele roda por registro, entremeado com a
// leitura, entao qualquer numero que saisse dali seria o tempo de outra coisa.
func TestAMapStageInventsNoDuration(t *testing.T) {
	var caixa []Envelope
	eventos, err := etapasDe(t, pipelineDeTeste(origemLenta{
		registros: []any{map[string]any{"id": 1}}, pausa: 20 * time.Millisecond,
	}, &caixa))
	if err != nil {
		t.Fatal(err)
	}
	fim := acharEtapa(t, eventos, "map", "done")
	if _, tem := fim["ms"]; tem {
		t.Errorf("a map stage reported a duration: %v", fim)
	}
}

// O que so o transform sabe: quantos entraram, quantos sairam, quantos foram
// pulados.
func TestAMapStageSaysHowManyItDropped(t *testing.T) {
	var caixa []Envelope
	eventos, err := etapasDe(t, pipelineDeTeste(origemContada{
		registros: []any{
			map[string]any{"id": 1},
			map[string]any{"sem_id": true},
			map[string]any{"id": 3},
		},
		leituras: new(int),
	}, &caixa))
	if err != nil {
		t.Fatal(err)
	}
	fim := acharEtapa(t, eventos, "map", "done")
	if fim["in"] != 3.0 || fim["out"] != 2.0 || fim["dropped"] != 1.0 {
		t.Errorf("contagem errada: %v", fim)
	}
}

// Fora do motor nao ha quem leia as etapas, e sujar o terminal de quem depura
// um fetcher seria custo sem retorno.
func TestOutsideTheEngineNothingIsAnnounced(t *testing.T) {
	var caixa []Envelope
	p := pipelineDeTeste(origemContada{
		registros: []any{map[string]any{"id": 1}}, leituras: new(int),
	}, &caixa)
	p.Run = RunContext{} // rodando a mao

	eventos, err := etapasDe(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventos) != 0 {
		t.Errorf("anunciou %d eventos rodando a mao: %v", len(eventos), eventos)
	}
	// E o pipeline continua funcionando igual.
	if len(caixa) != 1 {
		t.Errorf("carregou %d registros, esperado 1", len(caixa))
	}
}

// O teto existe porque o stream de log vira escrita em banco do outro lado: um
// pipeline em laco derrubaria o Postgres pelo caminho do log.
func TestThePhaseCap(t *testing.T) {
	var buf bytes.Buffer
	anterior := phaseOutput
	phaseOutput = &buf
	defer func() { phaseOutput = anterior }()

	r := newReporter(RunContext{ID: "run-1"})
	for i := 0; i < phaseCap*3; i++ {
		r.started(PhaseSource)
	}
	if n := strings.Count(buf.String(), phaseMarker); n != phaseCap {
		t.Errorf("emitiu %d linhas, o teto e %d", n, phaseCap)
	}
}

func acharEtapa(t *testing.T, eventos []map[string]any, nome, estado string) map[string]any {
	t.Helper()
	for _, ev := range eventos {
		if ev["name"] == nome && ev["state"] == estado {
			return ev
		}
	}
	t.Fatalf("nao achei a etapa %s:%s em %v", nome, estado, eventos)
	return nil
}

func duracaoDaEtapa(t *testing.T, eventos []map[string]any, nome string) float64 {
	t.Helper()
	ms, ok := acharEtapa(t, eventos, nome, "done")["ms"].(float64)
	if !ok {
		t.Fatalf("a etapa %s nao reportou duracao", nome)
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
	var caixa []Envelope
	eventos, err := etapasDe(t, &Pipeline{
		Name:   "fetcher",
		Source: Source{From: origemContada{registros: registros(), leituras: new(int)}},
		Stages: []Stage{
			Map(SkipWithout("provider")),
			Aggregate(Reduce{By: GroupBy("provider"), Agg: map[string]Aggregator{"n": Count()}}),
			Map(Compute("x", func(map[string]any) (any, error) { return 1, nil })),
		},
		Target: Target{To: destinoQueGuarda{recebido: &caixa}},
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

	nomes := []string{"check", "extract", "map", "aggregate", "map", "load"}
	for i, quero := range nomes {
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
	if depois := porIndice[4]; depois["in"] != 1.0 {
		t.Errorf("the map after the aggregation saw %v records, want 1", depois["in"])
	}
}

// The card is half a card without saying WHICH source and WHICH destination.
func TestTheSourceAndTargetSayWhichTheyAre(t *testing.T) {
	var caixa []Envelope
	eventos, err := etapasDe(t, pipelineDeTeste(origemContada{
		registros: registros(), leituras: new(int),
	}, &caixa))
	if err != nil {
		t.Fatal(err)
	}
	if d := acharEtapa(t, eventos, "extract", "done")["detail"]; d != "origem.teste" {
		t.Errorf("the source does not say which it is: %v", d)
	}
	if d := acharEtapa(t, eventos, "load", "done")["detail"]; d != "destino.teste" {
		t.Errorf("the destination does not say which it is: %v", d)
	}
}
