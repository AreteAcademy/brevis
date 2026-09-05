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
	anterior := saidaDasEtapas
	saidaDasEtapas = &buf
	defer func() { saidaDasEtapas = anterior }()

	err := runPipeline(context.Background(), p)

	var eventos []map[string]any
	for _, linha := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(linha, marcaEtapa) {
			continue
		}
		var ev map[string]any
		if e := json.Unmarshal([]byte(strings.TrimPrefix(linha, marcaEtapa)), &ev); e != nil {
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

func TestEtapasSaemNaOrdem(t *testing.T) {
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
		if ev["tipo"] == "sdk" {
			trilha = append(trilha, "sdk")
			continue
		}
		trilha = append(trilha, ev["nome"].(string)+":"+ev["estado"].(string))
	}
	querido := []string{
		"sdk",
		"check:running", "check:done",
		"extract:running",
		"transform:running",
		"extract:done", "transform:done",
		"load:running", "load:done",
	}
	if strings.Join(trilha, " ") != strings.Join(querido, " ") {
		t.Errorf("trilha:\n  %v\nesperada:\n  %v", trilha, querido)
	}
}

// O anuncio carrega a versao, porque um selo que so diz "SDK" e verdadeiro e
// inutil: a versao e o que responde "por que este passo se comporta diferente
// do vizinho" sem ninguem abrir o Dockerfile.
func TestAnuncioCarregaAVersao(t *testing.T) {
	var caixa []Envelope
	eventos, err := etapasDe(t, pipelineDeTeste(origemContada{
		registros: []any{map[string]any{"id": 1}}, leituras: new(int),
	}, &caixa))
	if err != nil {
		t.Fatal(err)
	}
	if len(eventos) == 0 || eventos[0]["tipo"] != "sdk" {
		t.Fatalf("o primeiro evento tem de ser o anuncio, veio %v", eventos)
	}
	if v, _ := eventos[0]["versao"].(string); v == "" {
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
func TestDuracaoDoExtractMedeAExtracaoDeVerdade(t *testing.T) {
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
func TestTransformNaoInventaDuracao(t *testing.T) {
	var caixa []Envelope
	eventos, err := etapasDe(t, pipelineDeTeste(origemLenta{
		registros: []any{map[string]any{"id": 1}}, pausa: 20 * time.Millisecond,
	}, &caixa))
	if err != nil {
		t.Fatal(err)
	}
	fim := acharEtapa(t, eventos, "transform", "done")
	if _, tem := fim["ms"]; tem {
		t.Errorf("o transform reportou duracao: %v", fim)
	}
}

// O que so o transform sabe: quantos entraram, quantos sairam, quantos foram
// pulados.
func TestTransformDizQuantosPulou(t *testing.T) {
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
	fim := acharEtapa(t, eventos, "transform", "done")
	if fim["entraram"] != 3.0 || fim["sairam"] != 2.0 || fim["pulados"] != 1.0 {
		t.Errorf("contagem errada: %v", fim)
	}
}

// Fora do motor nao ha quem leia as etapas, e sujar o terminal de quem depura
// um fetcher seria custo sem retorno.
func TestForaDoMotorNaoAnunciaNada(t *testing.T) {
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
func TestTetoDeEtapas(t *testing.T) {
	var buf bytes.Buffer
	anterior := saidaDasEtapas
	saidaDasEtapas = &buf
	defer func() { saidaDasEtapas = anterior }()

	r := novoRelator(RunContext{ID: "run-1"})
	for i := 0; i < tetoDeEtapas*3; i++ {
		r.comecou(EtapaExtract)
	}
	if n := strings.Count(buf.String(), marcaEtapa); n != tetoDeEtapas {
		t.Errorf("emitiu %d linhas, o teto e %d", n, tetoDeEtapas)
	}
}

func acharEtapa(t *testing.T, eventos []map[string]any, nome, estado string) map[string]any {
	t.Helper()
	for _, ev := range eventos {
		if ev["nome"] == nome && ev["estado"] == estado {
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
