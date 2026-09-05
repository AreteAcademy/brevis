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

// origemContada conta quantas vezes foi lida e pode recusar a segunda leitura.
//
// Recusar e o que torna o teste capaz de falhar: um checkpoint que nao poupasse
// a origem passaria despercebido se a origem simplesmente respondesse de novo.
type origemContada struct {
	registros []any
	leituras  *int
	soUmaVez  bool
}

func (origemContada) Describe() string { return "origem.teste" }

func (o origemContada) Read(context.Context, ReadOptions) (iter.Seq2[Envelope, error], error) {
	*o.leituras++
	if o.soUmaVez && *o.leituras > 1 {
		return nil, fmt.Errorf("a origem foi consultada %d vezes", *o.leituras)
	}
	regs := o.registros
	return func(yield func(Envelope, error) bool) {
		for _, r := range regs {
			if !yield(Envelope{Payload: r}, nil) {
				return
			}
		}
	}, nil
}

// destinoQueGuarda guarda o que recebeu, inclusive quando recusa a carga.
type destinoQueGuarda struct {
	recebido *[]Envelope
	falhar   bool
}

func (destinoQueGuarda) Describe() string { return "destino.teste" }

func (d destinoQueGuarda) Write(_ context.Context, envs []Envelope, _ WriteOptions) (*LoadResult, error) {
	*d.recebido = append(*d.recebido, envs...)
	if d.falhar {
		return &LoadResult{}, fmt.Errorf("o destino recusou a carga")
	}
	return &LoadResult{RowsLoaded: int64(len(envs)), Strategy: "teste"}, nil
}

func rodar(t *testing.T, p *Pipeline) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(anterior)

	err := runPipeline(context.Background(), p)
	return buf.String(), err
}

func registros() []any {
	return []any{
		map[string]any{"provider": "p", "entity": "e", "source_key": "a", "record_ts": "2026-09-05T00:00:00Z"},
		map[string]any{"provider": "p", "entity": "e", "source_key": "b", "record_ts": "2026-09-05T01:00:00Z"},
	}
}

// FASE 1 -- a segunda tentativa nao pode tocar na origem.
//
// E o pedido inteiro: o extract gastou a quota do fornecedor, o destino
// recusou, e a tentativa seguinte tem de carregar o mesmo dado sem voltar la.
func TestCheckpointNaSegundaTentativaNaoTocaNaOrigem(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	origem := origemContada{registros: registros(), leituras: &leituras, soUmaVez: true}

	var primeira []Envelope
	_, err := rodar(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: origem},
		Checkpoint: Checkpoint{At: dir},
		Target:     Target{To: destinoQueGuarda{recebido: &primeira, falhar: true}},
		Run:        RunContext{ID: "run-1", Attempt: 0},
	})
	if err == nil {
		t.Fatal("a primeira tentativa devia falhar no destino")
	}

	var segunda []Envelope
	log, err := rodar(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: origem},
		Checkpoint: Checkpoint{At: dir},
		Target:     Target{To: destinoQueGuarda{recebido: &segunda}},
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
	if !strings.Contains(log, "checkpoint=reaproveitado") {
		t.Errorf("o log nao diz que reaproveitou; uma economia invisivel e indistinguivel de nao ter economizado:\n%s", log)
	}
}

// FASE 2 (I1) -- sem manifesto nao ha retomada.
func TestCheckpointSemManifestoRefazOExtract(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	origem := origemContada{registros: registros(), leituras: &leituras}

	var caixa []Envelope
	p := func(tentativa int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: origem},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
			Run:        RunContext{ID: "run-2", Attempt: tentativa},
		}
	}
	if _, err := rodar(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}

	// O manifesto e o que autoriza a retomada. Sem ele o deposito e um extract
	// interrompido, e retomar dali carregaria metade dos dados em silencio.
	apagar(t, dir, "_completo")

	log, err := rodar(t, p(1))
	if err != nil {
		t.Fatalf("segunda tentativa: %v\n%s", err, log)
	}
	if leituras != 2 {
		t.Errorf("a origem foi lida %d vezes; sem manifesto o extract tem de ser refeito", leituras)
	}
}

// FASE 2 (I1) -- manifesto inteiro, parte faltando: recusa antes de carregar.
func TestCheckpointComParteFaltandoRefazOExtract(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	origem := origemContada{registros: registros(), leituras: &leituras}

	var caixa []Envelope
	p := func(tentativa int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: origem},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
			Run:        RunContext{ID: "run-3", Attempt: tentativa},
		}
	}
	if _, err := rodar(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}
	apagar(t, dir, "parte-00000.ndjson")

	log, err := rodar(t, p(1))
	if err != nil {
		t.Fatalf("segunda tentativa: %v\n%s", err, log)
	}
	if leituras != 2 {
		t.Errorf("a origem foi lida %d vezes; um deposito capenga tem de ser descartado", leituras)
	}
	if !strings.Contains(log, "checkpoint incompleto") {
		t.Errorf("descartar um checkpoint calado esconde a causa:\n%s", log)
	}
	// E o que foi carregado tem de ser o conjunto inteiro, nao o que sobrou.
	if len(caixa) != 4 {
		t.Errorf("carregou %d registros nas duas tentativas, esperado 4", len(caixa))
	}
}

// FASE 2 -- manifesto que mente na contagem falha ALTO.
//
// Isto so pode acontecer com o objeto adulterado depois de escrito. Seguir
// calado carregaria menos linhas do que a primeira tentativa carregou, e
// ninguem saberia.
func TestCheckpointComContagemErradaFalhaAlto(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	origem := origemContada{registros: registros(), leituras: &leituras}

	var caixa []Envelope
	p := func(tentativa int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: origem},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
			Run:        RunContext{ID: "run-4", Attempt: tentativa},
		}
	}
	if _, err := rodar(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}

	caminho := achar(t, dir, "_completo")
	var m map[string]any
	lerJSON(t, caminho, &m)
	m["registros"] = 99
	gravarJSON(t, caminho, m)

	_, err := rodar(t, p(1))
	if err == nil {
		t.Fatal("um checkpoint que mente na contagem tem de falhar, nao carregar menos em silencio")
	}
	if !strings.Contains(err.Error(), "corrompido") {
		t.Errorf("a mensagem nao diz o que houve: %v", err)
	}
}

// FASE 3 (I3) -- os ingestion_id de uma retomada sao identicos.
//
// E a garantia que o pedido chama de "carregar o mesmo dado".
func TestCheckpointPreservaOIngestionID(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	origem := origemContada{registros: registros(), leituras: &leituras, soUmaVez: true}

	var daOrigem, doCheckpoint []Envelope
	p := func(tentativa int, caixa *[]Envelope) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: origem},
			Checkpoint: Checkpoint{At: dir},
			Transform:  []Transformer{IngestionID()},
			Target:     Target{To: destinoQueGuarda{recebido: caixa}},
			Run:        RunContext{ID: "run-5", Attempt: tentativa},
		}
	}
	if _, err := rodar(t, p(0, &daOrigem)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}
	if log, err := rodar(t, p(1, &doCheckpoint)); err != nil {
		t.Fatalf("retomada: %v\n%s", err, log)
	}

	if len(daOrigem) != len(doCheckpoint) {
		t.Fatalf("%d registros da origem contra %d do checkpoint", len(daOrigem), len(doCheckpoint))
	}
	for i := range daOrigem {
		a := daOrigem[i].Payload.(map[string]any)["ingestion_id"]
		b := doCheckpoint[i].Payload.(map[string]any)["ingestion_id"]
		if a != b {
			t.Errorf("registro %d: id %v da origem, %v do checkpoint", i, a, b)
		}
		if a == nil || a == "" {
			t.Fatalf("registro %d nao tem ingestion_id; o teste nao esta comparando nada", i)
		}
	}
}

// FASE 3 -- o literal do numero sobrevive a volta pelo NDJSON.
//
// Um payload com json.Number carrega `19.0`; relido como float64 ele viraria
// "19" no asText, e a retomada gravaria um ingestion_id diferente do da
// primeira tentativa. O modo fica no manifesto.
func TestCheckpointPreservaOLiteralDoNumero(t *testing.T) {
	dir := t.TempDir()
	var leituras int
	origem := origemContada{
		registros: []any{map[string]any{"id": json.Number("19.0"), "n": json.Number("1e21")}},
		leituras:  &leituras, soUmaVez: true,
	}

	var caixa []Envelope
	p := func(tentativa int) *Pipeline {
		return &Pipeline{
			Name: "fetcher", Source: Source{From: origem},
			Checkpoint: Checkpoint{At: dir},
			Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
			Run:        RunContext{ID: "run-6", Attempt: tentativa},
		}
	}
	if _, err := rodar(t, p(0)); err != nil {
		t.Fatalf("primeira tentativa: %v", err)
	}
	if log, err := rodar(t, p(1)); err != nil {
		t.Fatalf("retomada: %v\n%s", err, log)
	}

	if len(caixa) != 2 {
		t.Fatalf("carregou %d registros, esperado 2", len(caixa))
	}
	for i, env := range caixa {
		row := env.Payload.(map[string]any)
		if got := asText(row["id"]); got != "19.0" {
			t.Errorf("registro %d: id virou %q, esperado \"19.0\"", i, got)
		}
		if got := asText(row["n"]); got != "1e21" {
			t.Errorf("registro %d: n virou %q, esperado \"1e21\"", i, got)
		}
	}
}

// FASE 4 (I5) -- um deposito que nao grava avisa, e a execucao segue.
//
// O checkpoint e uma apolice, nao o produto. Morrer por causa do seguro seria
// trocar uma falha rara por uma falha em toda execucao.
func TestCheckpointQueNaoGravaNaoDerrubaAExecucao(t *testing.T) {
	var leituras int
	var caixa []Envelope
	log, err := rodar(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: origemContada{registros: registros(), leituras: &leituras}},
		Checkpoint: Checkpoint{At: "s3://balde/cp", Store: storeQueRecusa{}},
		Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
		Run:        RunContext{ID: "run-7", Attempt: 0},
	})
	if err != nil {
		t.Fatalf("a execucao morreu por causa do checkpoint: %v\n%s", err, log)
	}
	if len(caixa) != 2 {
		t.Errorf("carregou %d registros, esperado 2", len(caixa))
	}
	if !strings.Contains(log, "checkpoint_falhou") {
		t.Errorf("a falha do deposito precisa aparecer no resultado:\n%s", log)
	}
}

// FASE 4 (I5) -- falhar no MEIO tambem nao derruba, e nao repete a origem.
//
// Aqui o `_inicio` grava e as partes nao. O fluxo degrada: cede o que ja virou
// objeto, o que ficou no buffer, e segue direto da origem -- sem refazer o
// extract, que e justamente o que se estava tentando poupar.
func TestCheckpointQueFalhaNoMeioDegradaSemRepetirAOrigem(t *testing.T) {
	var leituras int
	var caixa []Envelope
	log, err := rodar(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: origemContada{registros: registros(), leituras: &leituras, soUmaVez: true}},
		Checkpoint: Checkpoint{At: "s3://balde/cp", Store: storeQueRecusaPartes{}},
		Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
		Run:        RunContext{ID: "run-8", Attempt: 0},
	})
	if err != nil {
		t.Fatalf("a execucao morreu por causa do checkpoint: %v\n%s", err, log)
	}
	if leituras != 1 {
		t.Errorf("a origem foi lida %d vezes; degradar nao pode custar uma segunda extracao", leituras)
	}
	if len(caixa) != 2 {
		t.Errorf("carregou %d registros, esperado 2 -- degradar nao pode perder linha", len(caixa))
	}
	if !strings.Contains(log, "checkpoint_falhou") {
		t.Errorf("a interrupcao precisa aparecer no resultado:\n%s", log)
	}
}

// Fora do motor nao ha chave estavel, e isso e DITO.
func TestCheckpointForaDoMotorAvisaEmVezDeIgnorar(t *testing.T) {
	var leituras int
	var caixa []Envelope
	log, err := rodar(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: origemContada{registros: registros(), leituras: &leituras}},
		Checkpoint: Checkpoint{At: t.TempDir()},
		Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
		Run:        RunContext{}, // sem id: rodando a mao
	})
	if err != nil {
		t.Fatalf("rodar a mao nao pode falhar: %v", err)
	}
	if !strings.Contains(log, "checkpoint desligado") {
		t.Errorf("desligar em silencio esconde uma configuracao que nao esta valendo:\n%s", log)
	}
}

// Um esquema que nao casa com o Store e ERRO, nao aviso: ele nunca vai gravar.
func TestCheckpointComStoreErradoEErro(t *testing.T) {
	var leituras int
	var caixa []Envelope
	_, err := rodar(t, &Pipeline{
		Name:       "fetcher",
		Source:     Source{From: origemContada{registros: registros(), leituras: &leituras}},
		Checkpoint: Checkpoint{At: "gs://balde/cp", Store: storeQueRecusa{}}, // store e s3
		Target:     Target{To: destinoQueGuarda{recebido: &caixa}},
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

// storeQueRecusaPartes deixa reservar e recusa as partes: e a falha que
// aparece no meio da extracao, depois de a quota ja ter sido gasta.
type storeQueRecusaPartes struct{ storeQueRecusa }

func (storeQueRecusaPartes) Create(_ context.Context, _, chave string, _ io.Reader) error {
	if strings.Contains(chave, "parte-") {
		return fmt.Errorf("balde cheio")
	}
	return nil
}

// --- utilitarios de arquivo ---

func achar(t *testing.T, raiz, nome string) string {
	t.Helper()
	var achado string
	err := filepath.Walk(raiz, func(p string, _ os.FileInfo, err error) error {
		if err == nil && filepath.Base(p) == nome {
			achado = p
		}
		return nil
	})
	if err != nil || achado == "" {
		t.Fatalf("nao achei %q em %s", nome, raiz)
	}
	return achado
}

func apagar(t *testing.T, raiz, nome string) {
	t.Helper()
	if err := os.Remove(achar(t, raiz, nome)); err != nil {
		t.Fatal(err)
	}
}

func lerJSON(t *testing.T, caminho string, v any) {
	t.Helper()
	data, err := os.ReadFile(caminho) //nolint:gosec // caminho de teste
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func gravarJSON(t *testing.T, caminho string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caminho, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
