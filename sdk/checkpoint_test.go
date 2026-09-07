package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countedSource counts how many times it was read and can refuse the second
// read.
//
// Refusing is what makes the test able to fail: a checkpoint that did not spare
// the source would go unnoticed if the source simply answered again.
type countedSource struct {
	records  []any
	leituras *int
	soUmaVez bool
}

func (countedSource) Describe() string { return "origem.teste" }

func (o countedSource) Read(context.Context, ReadOptions) (iter.Seq2[Envelope, error], error) {
	*o.leituras++
	if o.soUmaVez && *o.leituras > 1 {
		return nil, fmt.Errorf("a origem foi consultada %d vezes", *o.leituras)
	}
	regs := o.records
	return func(yield func(Envelope, error) bool) {
		for _, r := range regs {
			if !yield(Envelope{Payload: r}, nil) {
				return
			}
		}
	}, nil
}

// keepingTarget stores what it received, including when it refuses the load.
type keepingTarget struct {
	received *[]Envelope
	fail     bool
}

func (keepingTarget) Describe() string { return "destino.teste" }

func (d keepingTarget) Write(_ context.Context, envs []Envelope, _ WriteOptions) (*LoadResult, error) {
	*d.received = append(*d.received, envs...)
	if d.fail {
		return &LoadResult{}, fmt.Errorf("o destino recusou a carga")
	}
	return &LoadResult{RowsLoaded: int64(len(envs)), Strategy: "teste"}, nil
}

func runIt(t *testing.T, p *Pipeline) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	err := runPipeline(context.Background(), p)
	return buf.String(), err
}

func twoRecords() []any {
	return []any{
		map[string]any{"provider": "p", "entity": "e", "source_key": "a", "record_ts": "2026-09-05T00:00:00Z"},
		map[string]any{"provider": "p", "entity": "e", "source_key": "b", "record_ts": "2026-09-05T01:00:00Z"},
	}
}

// PHASE 1 -- the second attempt must not touch the source.
//
// It is the whole request: the extract spent the vendor's quota, the
// destination
// refused, and the next attempt has to load the same data without going back.
func TestOnTheSecondAttemptTheCheckpointDoesNotTouchTheSource(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	src := countedSource{records: twoRecords(), leituras: &leituras, soUmaVez: true}

	var first []Envelope
	_, err := runIt(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: src},
		Checkpoint: Checkpoint{At: dir},
		Target:     Target{To: keepingTarget{received: &first, fail: true}},
		Run:        RunContext{ID: "run-1", Attempt: 0},
	})
	if err == nil {
		t.Fatal("a primeira tentativa devia falhar no destino")
	}

	var segunda []Envelope
	log, err := runIt(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: src},
		Checkpoint: Checkpoint{At: dir},
		Target:     Target{To: keepingTarget{received: &segunda}},
		Run:        RunContext{ID: "run-1", Attempt: 1},
	})
	if err != nil {
		t.Fatalf("a segunda tentativa falhou: %v\n%s", err, log)
	}

	if leituras != 1 {
		t.Errorf("a origem foi lida %d vezes; o checkpoint nao poupou nada", leituras)
	}
	if len(segunda) != 2 {
		t.Fatalf("a retomada carregou %d registros, esperado 2", len(segunda))
	}
	if !strings.Contains(log, "checkpoint=reused") {
		t.Errorf("o log nao diz que reaproveitou; uma economia invisivel e indistinguivel de nao ter economizado:\n%s", log)
	}
}

// PHASE 2 (I1) -- with no manifest there is no resume.
func TestACheckpointWithNoManifestRedoesTheExtract(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	src := countedSource{records: twoRecords(), leituras: &leituras}

	var box []Envelope
	p := func(attempt int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: src},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: keepingTarget{received: &box}},
			Run:        RunContext{ID: "run-2", Attempt: attempt},
		}
	}
	if _, err := runIt(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}

	// The manifest is what authorizes the resume. Without it the depot is an
	// extract
	// interrupted, and resuming from there would load half the data in
	// silence.
	remove(t, dir, "_completo")

	log, err := runIt(t, p(1))
	if err != nil {
		t.Fatalf("segunda tentativa: %v\n%s", err, log)
	}
	if leituras != 2 {
		t.Errorf("a origem foi lida %d vezes; sem manifesto o extract tem de ser refeito", leituras)
	}
}

// FASE 2 (I1) -- manifesto inteiro, parte faltando: recusa antes de carregar.
func TestACheckpointWithAMissingPartRedoesTheExtract(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	src := countedSource{records: twoRecords(), leituras: &leituras}

	var box []Envelope
	p := func(attempt int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: src},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: keepingTarget{received: &box}},
			Run:        RunContext{ID: "run-3", Attempt: attempt},
		}
	}
	if _, err := runIt(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}
	remove(t, dir, "parte-00000.ndjson")

	log, err := runIt(t, p(1))
	if err != nil {
		t.Fatalf("segunda tentativa: %v\n%s", err, log)
	}
	if leituras != 2 {
		t.Errorf("a origem foi lida %d vezes; um deposito capenga tem de ser descartado", leituras)
	}
	if !strings.Contains(log, "checkpoint incomplete") {
		t.Errorf("descartar um checkpoint calado esconde a causa:\n%s", log)
	}
	// And what was loaded has to be the whole set, not what was left over.
	if len(box) != 4 {
		t.Errorf("carregou %d registros nas duas tentativas, esperado 4", len(box))
	}
}

// PHASE 2 -- a manifest that lies about the count fails LOUDLY.
//
// This can only happen with the object tampered with after it was written.
// Carrying on quietly would load fewer rows than the first attempt loaded,
// and
// ninguem saberia.
func TestACheckpointWithAWrongCountFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	src := countedSource{records: twoRecords(), leituras: &leituras}

	var box []Envelope
	p := func(attempt int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: src},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: keepingTarget{received: &box}},
			Run:        RunContext{ID: "run-4", Attempt: attempt},
		}
	}
	if _, err := runIt(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}

	path := find(t, dir, "_completo")
	var m map[string]any
	lerJSON(t, path, &m)
	m["registros"] = 99
	gravarJSON(t, path, m)

	_, err := runIt(t, p(1))
	if err == nil {
		t.Fatal("um checkpoint que mente na contagem tem de falhar, nao carregar menos em silencio")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("a mensagem nao diz o que houve: %v", err)
	}
}

// PHASE 3 (I3) -- a resumed attempt's ingestion_ids are identical.
//
// It is the guarantee the request calls "loading the same data".
func TestTheCheckpointPreservesTheIngestionID(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	src := countedSource{records: twoRecords(), leituras: &leituras, soUmaVez: true}

	var fromSource, doCheckpoint []Envelope
	p := func(attempt int, box *[]Envelope) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: src},
			Checkpoint: Checkpoint{At: dir},
			Transform:  []Transformer{IngestionID()},
			Target:     Target{To: keepingTarget{received: box}},
			Run:        RunContext{ID: "run-5", Attempt: attempt},
		}
	}
	if _, err := runIt(t, p(0, &fromSource)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}
	if log, err := runIt(t, p(1, &doCheckpoint)); err != nil {
		t.Fatalf("retomada: %v\n%s", err, log)
	}

	if len(fromSource) != len(doCheckpoint) {
		t.Fatalf("%d registros da origem contra %d do checkpoint", len(fromSource), len(doCheckpoint))
	}
	for i := range fromSource {
		a := fromSource[i].Payload.(map[string]any)["ingestion_id"]
		b := doCheckpoint[i].Payload.(map[string]any)["ingestion_id"]
		if a != b {
			t.Errorf("registro %d: id %v da origem, %v do checkpoint", i, a, b)
		}
		if a == nil || a == "" {
			t.Fatalf("registro %d nao tem ingestion_id; o teste nao esta comparando nada", i)
		}
	}
}

// PHASE 3 -- the number's literal survives the round trip through NDJSON.
//
// A payload with a json.Number carries `19.0`; read back as a float64 it would
// become
// "19" in asText, and the resume would write an ingestion_id different from
// first attempt. The mode is recorded in the manifest.
func TestTheCheckpointPreservesTheNumbersLiteral(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	src := countedSource{
		records:  []any{map[string]any{"id": json.Number("19.0"), "n": json.Number("1e21")}},
		leituras: &leituras, soUmaVez: true,
	}

	var box []Envelope
	p := func(attempt int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: src},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: keepingTarget{received: &box}},
			Run:        RunContext{ID: "run-6", Attempt: attempt},
		}
	}
	if _, err := runIt(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}
	if log, err := runIt(t, p(1)); err != nil {
		t.Fatalf("retomada: %v\n%s", err, log)
	}

	if len(box) != 2 {
		t.Fatalf("carregou %d registros, esperado 2", len(box))
	}
	for i, env := range box {
		row := env.Payload.(map[string]any)
		if got := asText(row["id"]); got != "19.0" {
			t.Errorf("registro %d: id virou %q, esperado \"19.0\"", i, got)
		}
		if got := asText(row["n"]); got != "1e21" {
			t.Errorf("registro %d: n virou %q, esperado \"1e21\"", i, got)
		}
	}
}

// PHASE 4 (I5) -- a depot that cannot write warns, and the run goes on.
//
// The checkpoint is an insurance policy, not the product. Dying because of the
// insurance would be
// trading a rare failure for a failure on every run.
func TestACheckpointThatCannotWriteDoesNotFailTheRun(t *testing.T) {
	var leituras int
	var box []Envelope
	log, err := runIt(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: countedSource{records: twoRecords(), leituras: &leituras}},
		Checkpoint: Checkpoint{At: "s3://balde/cp", Store: storeQueRecusa{}},
		Target:     Target{To: keepingTarget{received: &box}},
		Run:        RunContext{ID: "run-7", Attempt: 0},
	})
	if err != nil {
		t.Fatalf("a execucao morreu por causa do checkpoint: %v\n%s", err, log)
	}
	if len(box) != 2 {
		t.Errorf("carregou %d registros, esperado 2", len(box))
	}
	if !strings.Contains(log, "checkpoint_failed") {
		t.Errorf("a falha do deposito precisa aparecer no resultado:\n%s", log)
	}
}

// PHASE 4 (I5) -- failing MIDWAY does not fail the run either, and does not
// re-read the source.
//
// Here `_inicio` writes and the parts do not. The stream degrades: it yields
// what already became
// object, what stayed in the buffer, and carries on straight from the source --
// without redoing the extract, which is precisely what was being spared.
func TestACheckpointFailingMidwayDegradesWithoutRereadingTheSource(t *testing.T) {
	var leituras int
	var box []Envelope
	log, err := runIt(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: countedSource{records: twoRecords(), leituras: &leituras, soUmaVez: true}},
		Checkpoint: Checkpoint{At: "s3://balde/cp", Store: storeRefusingParts{}},
		Target:     Target{To: keepingTarget{received: &box}},
		Run:        RunContext{ID: "run-8", Attempt: 0},
	})
	if err != nil {
		t.Fatalf("a execucao morreu por causa do checkpoint: %v\n%s", err, log)
	}
	if leituras != 1 {
		t.Errorf("a origem foi lida %d vezes; degradar nao pode custar uma segunda extracao", leituras)
	}
	if len(box) != 2 {
		t.Errorf("carregou %d registros, esperado 2 -- degradar nao pode perder linha", len(box))
	}
	if !strings.Contains(log, "checkpoint_failed") {
		t.Errorf("a interrupcao precisa aparecer no resultado:\n%s", log)
	}
}

// Outside the engine there is no stable key, and that is SAID.
func TestOutsideTheEngineTheCheckpointWarnsInsteadOfIgnoring(t *testing.T) {
	var leituras int
	var box []Envelope
	log, err := runIt(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: countedSource{records: twoRecords(), leituras: &leituras}},
		Checkpoint: Checkpoint{At: t.TempDir()},
		Target:     Target{To: keepingTarget{received: &box}},
		Run:        RunContext{}, // no id: running by hand
	})
	if err != nil {
		t.Fatalf("rodar a mao nao pode falhar: %v", err)
	}
	if !strings.Contains(log, "checkpoint off") {
		t.Errorf("desligar em silencio esconde uma configuracao que nao esta valendo:\n%s", log)
	}
}

// A scheme that does not match the Store is an ERROR, not a warning: it will
// never write.
func TestACheckpointWithTheWrongStoreIsAnError(t *testing.T) {
	var leituras int
	var box []Envelope
	_, err := runIt(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: countedSource{records: twoRecords(), leituras: &leituras}},
		Checkpoint: Checkpoint{At: "gs://balde/cp", Store: storeQueRecusa{}}, // store e s3
		Target:     Target{To: keepingTarget{received: &box}},
		Run:        RunContext{ID: "run-9"},
	})
	if err == nil {
		t.Fatal("um Store que nunca vai atender o caminho tem de ser recusado na hora")
	}
	if !strings.Contains(err.Error(), "gs") || !strings.Contains(err.Error(), "s3") {
		t.Errorf("a mensagem tem de nomear os dois lados: %v", err)
	}
	if leituras != 0 {
		t.Error("recusou depois de gastar a origem")
	}
}

// --- stores de teste ---

type storeQueRecusa struct{}

func (storeQueRecusa) Scheme() string { return "s3" }
func (storeQueRecusa) List(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (storeQueRecusa) Open(context.Context, string, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("sem permissao de leitura")
}
func (storeQueRecusa) Create(context.Context, string, string, io.Reader) error {
	return fmt.Errorf("sem permissao de escrita")
}

// storeRefusingParts allows the reservation and refuses the parts: it is the
// failure that shows up mid-extraction, after the quota has already been
// spent.
type storeRefusingParts struct{ storeQueRecusa }

func (storeRefusingParts) Create(_ context.Context, _, key string, _ io.Reader) error {
	if strings.Contains(key, "parte-") {
		return fmt.Errorf("balde cheio")
	}
	return nil
}

// --- file helpers ---

func find(t *testing.T, raiz, name string) string {
	t.Helper()
	var achado string
	err := filepath.Walk(raiz, func(p string, _ os.FileInfo, err error) error {
		if err == nil && filepath.Base(p) == name {
			achado = p
		}
		return nil
	})
	if err != nil || achado == "" {
		t.Fatalf("nao achei %q em %s", name, raiz)
	}
	return achado
}

func remove(t *testing.T, raiz, name string) {
	t.Helper()
	if err := os.Remove(find(t, raiz, name)); err != nil {
		t.Fatal(err)
	}
}

func lerJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // caminho de teste
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func gravarJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
