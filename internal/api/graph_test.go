package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// Grafo em diamante: b e c dependem de a, d depende dos dois. O formato importa
// porque e o menor caso em que layout errado aparece — b e c TEM que sair na
// mesma coluna, ou o desenho contradiz o que o executor faz.
func diamante() wf.Workflow {
	return wf.Workflow{
		Slug: "diamante",
		Nodes: []wf.Node{
			{ID: "a", Run: "echo a"}, {ID: "b", Run: "echo b"},
			{ID: "c", Action: "docker.run"}, {ID: "d", Run: "echo d"},
		},
		Edges: []wf.Edge{{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"}},
	}
}

type defsFake struct {
	w   wf.Workflow
	err error
}

func (d defsFake) Definicao(context.Context, string) (wf.Workflow, error) { return d.w, d.err }

type execsFake struct {
	run     dom.Run
	estados map[string]postgres.EstadoNo
	err     error
}

func (e execsFake) Buscar(context.Context, uuid.UUID) (dom.Run, error) { return e.run, e.err }
func (e execsFake) LogsDaRun(context.Context, uuid.UUID) ([]postgres.LogDoPasso, error) {
	return nil, nil
}

func (e execsFake) EstadoDosNos(context.Context, uuid.UUID) (map[string]postgres.EstadoNo, error) {
	return e.estados, nil
}

type grafo struct {
	Slug     string `json:"slug"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Terminal bool   `json:"terminal"`
	Nodes    []struct {
		ID         string             `json:"id"`
		Type       string             `json:"type"`
		Position   struct{ X, Y int } `json:"position"`
		Data       map[string]any     `json:"data"`
		ParentID   string             `json:"parentId"`
		Extent     string             `json:"extent"`
		Style      map[string]any     `json:"style"`
		Selectable *bool              `json:"selectable"`
	} `json:"nodes"`
	Edges []struct {
		ID       string `json:"id"`
		Source   string `json:"source"`
		Target   string `json:"target"`
		Animated bool   `json:"animated"`
	} `json:"edges"`
}

func pedir(t *testing.T, ui *api.UI, caminho string) (*http.Response, grafo) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, caminho, nil))

	res := rec.Result()
	var g grafo
	corpo, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(corpo, &g); err != nil {
			t.Fatalf("json invalido: %v — %s", err, corpo)
		}
	}
	return res, g
}

func novaUI(d api.Definicoes, e api.Execucoes) *api.UI {
	return api.NewUI(nil, d, e, nil, branding.Padrao(), slog.New(slog.DiscardHandler))
}

func TestGrafoDoWorkflowPoeNiveisEmColunas(t *testing.T) {
	ui := novaUI(defsFake{w: diamante()}, execsFake{})
	res, g := pedir(t, ui, "/api/workflows/diamante/graph")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quero 200", res.StatusCode)
	}
	if len(g.Nodes) != 4 || len(g.Edges) != 4 {
		t.Fatalf("nodes=%d edges=%d, quero 4 e 4", len(g.Nodes), len(g.Edges))
	}

	x := map[string]int{}
	y := map[string]int{}
	for _, n := range g.Nodes {
		x[n.ID], y[n.ID] = n.Position.X, n.Position.Y
		if n.Type != "brevis" {
			t.Errorf("no %s com type %q, quero brevis (o custom node)", n.ID, n.Type)
		}
	}
	if !(x["a"] < x["b"] && x["b"] < x["d"]) {
		t.Errorf("colunas fora de ordem: a=%d b=%d d=%d", x["a"], x["b"], x["d"])
	}
	if x["b"] != x["c"] {
		t.Errorf("b e c rodam em paralelo mas sairam em colunas diferentes: %d e %d", x["b"], x["c"])
	}
	if y["b"] == y["c"] {
		t.Errorf("b e c sairam sobrepostos em y=%d", y["b"])
	}
	if y["a"] != y["d"] {
		t.Errorf("niveis de um no so deviam ficar centrados: a=%d d=%d", y["a"], y["d"])
	}
}

// Sem execucao, todo no e "pending" — a tela de um workflow que nunca rodou nao
// pode herdar estado de lugar nenhum.
func TestGrafoDoWorkflowNaoTemEstado(t *testing.T) {
	ui := novaUI(defsFake{w: diamante()}, execsFake{})
	_, g := pedir(t, ui, "/api/workflows/diamante/graph")

	for _, n := range g.Nodes {
		if n.Data["status"] != "pending" {
			t.Errorf("no %s com status %v, quero pending", n.ID, n.Data["status"])
		}
	}
	if g.RunID != "" {
		t.Errorf("run_id = %q num grafo sem execucao", g.RunID)
	}
	if g.Nodes[0].Data["acao"] == nil {
		t.Error("o card perdeu o rotulo da acao")
	}
}

func TestGrafoDaRunAplicaEstadoPorNo(t *testing.T) {
	id := uuid.New()
	def, _ := json.Marshal(diamante())
	saida := 2
	ui := novaUI(defsFake{err: errors.New("nao deve consultar a definicao publicada")}, execsFake{
		run: dom.Run{ID: id, WorkflowSlug: "diamante", Status: dom.StatusRunning, Definicao: def},
		estados: map[string]postgres.EstadoNo{
			"a": {NodeID: "a", Status: "success", DuracaoMs: 1200},
			"b": {NodeID: "b", Status: "running"},
			"c": {NodeID: "c", Status: "failed", Tentativa: 2, ExitCode: &saida, Erro: "boom"},
		},
	})

	res, g := pedir(t, ui, "/api/runs/"+id.String()+"/graph")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quero 200", res.StatusCode)
	}
	if g.RunID != id.String() || g.Status != "running" {
		t.Fatalf("cabecalho errado: run=%q status=%q", g.RunID, g.Status)
	}
	if g.Terminal {
		t.Error("run em execucao marcada como terminal: a UI pararia de atualizar")
	}

	porID := map[string]map[string]any{}
	for _, n := range g.Nodes {
		porID[n.ID] = n.Data
	}
	if porID["a"]["status"] != "success" || porID["a"]["duracao_ms"] != float64(1200) {
		t.Errorf("no a: %v", porID["a"])
	}
	if porID["c"]["erro"] != "boom" || porID["c"]["exit_code"] != float64(2) {
		t.Errorf("no c perdeu erro/exit code: %v", porID["c"])
	}
	// d nunca rodou; tem que continuar cinza em vez de herdar o estado do pai.
	if porID["d"]["status"] != "pending" {
		t.Errorf("no d: %v", porID["d"])
	}

	for _, e := range g.Edges {
		if e.Target == "b" && !e.Animated {
			t.Error("aresta que chega no no em execucao devia estar animada")
		}
		if e.Target == "d" && e.Animated {
			t.Error("aresta para no parado nao deve animar")
		}
	}
}

// Grafo terminal precisa dizer isso no JSON: e o sinal que faz a ilha parar de
// consultar. Sem ele, cada run concluida deixa um poll eterno de 2 em 2s.
func TestGrafoDaRunMarcaTerminal(t *testing.T) {
	id := uuid.New()
	def, _ := json.Marshal(diamante())
	ui := novaUI(defsFake{}, execsFake{
		run: dom.Run{ID: id, Status: dom.StatusSuccess, Definicao: def},
	})
	_, g := pedir(t, ui, "/api/runs/"+id.String()+"/graph")
	if !g.Terminal {
		t.Error("run em success nao foi marcada como terminal")
	}
}

func TestGrafoRecusaEntradasInvalidas(t *testing.T) {
	casos := []struct {
		nome     string
		ui       *api.UI
		caminho  string
		esperado int
	}{
		{"workflow inexistente", novaUI(defsFake{err: errors.New("sem linhas")}, execsFake{}),
			"/api/workflows/fantasma/graph", http.StatusNotFound},
		{"uuid malformado", novaUI(defsFake{}, execsFake{}),
			"/api/runs/nao-e-uuid/graph", http.StatusBadRequest},
		{"run inexistente", novaUI(defsFake{}, execsFake{err: errors.New("sem linhas")}),
			"/api/runs/" + uuid.New().String() + "/graph", http.StatusNotFound},
		{"ciclo gravado no banco", novaUI(defsFake{w: wf.Workflow{
			Slug:  "ciclo",
			Nodes: []wf.Node{{ID: "a", Run: "x"}, {ID: "b", Run: "y"}},
			Edges: []wf.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
		}}, execsFake{}), "/api/workflows/ciclo/graph", http.StatusUnprocessableEntity},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			res, _ := pedir(t, c.ui, c.caminho)
			if res.StatusCode != c.esperado {
				t.Errorf("status = %d, quero %d", res.StatusCode, c.esperado)
			}
		})
	}
}

// etapas de um passo do SDK, como o coletor do runner as grava.
func quatroEtapas() []postgres.Etapa {
	ms := int64(2400)
	return []postgres.Etapa{
		{Nome: "check", Estado: "done"},
		{Nome: "extract", Estado: "done", Ms: &ms, Numeros: map[string]any{"paginas": 300}},
		{Nome: "transform", Estado: "done", Numeros: map[string]any{"pulados": 13}},
		{Nome: "load", Estado: "running"},
	}
}

func grafoComEtapas(t *testing.T, estados map[string]postgres.EstadoNo) grafo {
	t.Helper()
	id := uuid.New()
	def, _ := json.Marshal(diamante())
	ui := novaUI(defsFake{}, execsFake{
		run:     dom.Run{ID: id, WorkflowSlug: "diamante", Status: dom.StatusRunning, Definicao: def},
		estados: estados,
	})
	res, g := pedir(t, ui, "/api/runs/"+id.String()+"/graph")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	return g
}

// Um passo do SDK vira um GRUPO, com as etapas dentro dele. As arestas do DAG
// continuam entre passos, entao o grupo ocupa uma coluna so -- e "mesma coluna
// significa rodar em paralelo" continua verdade.
func TestPassoDoSDKViraGrupoComAsEtapasDentro(t *testing.T) {
	g := grafoComEtapas(t, map[string]postgres.EstadoNo{
		"b": {NodeID: "b", Status: "running", Etapas: quatroEtapas(), SdkVersao: "v0.44.1"},
	})

	var pai int = -1
	var filhos []int
	for i, n := range g.Nodes {
		if n.ID == "b" {
			pai = i
		}
		if n.ParentID == "b" {
			filhos = append(filhos, i)
		}
	}
	if pai < 0 {
		t.Fatal("o passo sumiu do grafo")
	}
	if len(filhos) != 4 {
		t.Fatalf("saiu com %d etapas, esperado 4", len(filhos))
	}

	// O React Flow exige o pai ANTES dos filhos no array.
	for _, f := range filhos {
		if f < pai {
			t.Fatal("um filho saiu antes do pai; o React Flow nao monta o grupo")
		}
	}

	altura, ok := g.Nodes[pai].Style["height"].(float64)
	if !ok || altura <= 0 {
		t.Fatalf("o grupo saiu sem altura declarada: %v", g.Nodes[pai].Style)
	}
	for _, f := range filhos {
		n := g.Nodes[f]
		if n.Type != "etapa" || n.Extent != "parent" {
			t.Errorf("filho mal formado: %+v", n)
		}
		if n.Selectable == nil || *n.Selectable {
			t.Errorf("etapa selecionavel abriria um painel vazio: %+v", n)
		}
		if float64(n.Position.Y)+26 > altura {
			t.Errorf("a etapa %s vaza do grupo: y=%d, altura=%v", n.ID, n.Position.Y, altura)
		}
	}
}

// O custo real do aninhamento: a coluna era centrada assumindo altura FIXA, e
// um grupo expandido passava por cima do vizinho.
//
// A altura de cada no e medida pelo que ele DESENHA -- a ultima etapa dentro
// dele -- e nao pela altura que ele declara. Conferir contra a declarada seria
// conferir o layout consigo mesmo: quem errasse as duas juntas passaria.
func TestColunaNaoSobrepoeComUmNoExpandido(t *testing.T) {
	g := grafoComEtapas(t, map[string]postgres.EstadoNo{
		"b": {NodeID: "b", Status: "running", Etapas: quatroEtapas(), SdkVersao: "v0.44.1"},
		"c": {NodeID: "c", Status: "pending"},
	})

	// O fundo de cada no, medido pelos filhos que ele carrega.
	fundo := map[string]int{}
	for _, n := range g.Nodes {
		if n.ParentID == "" {
			continue
		}
		if b := n.Position.Y + 26; b > fundo[n.ParentID] {
			fundo[n.ParentID] = b
		}
	}

	altura := func(id string) int {
		if f := fundo[id]; f > 0 {
			return f + 10 // a folga de rodape do grupo
		}
		return 84 // um card sem etapas
	}

	var b, c int
	temB, temC := false, false
	for _, n := range g.Nodes {
		switch {
		case n.ID == "b" && n.ParentID == "":
			b, temB = n.Position.Y, true
		case n.ID == "c" && n.ParentID == "":
			c, temC = n.Position.Y, true
		}
	}
	if !temB || !temC {
		t.Fatal("b e c precisam estar no grafo")
	}

	// b e c estao no MESMO nivel do diamante: um tem de acabar antes de o
	// outro comecar.
	cima, baixo, alturaDeCima := b, c, altura("b")
	if c < b {
		cima, baixo, alturaDeCima = c, b, altura("c")
	}
	if cima+alturaDeCima > baixo {
		t.Errorf("os nos se sobrepoem: um vai de %d a %d, o outro comeca em %d",
			cima, cima+alturaDeCima, baixo)
	}
}

// O selo diz que o passo foi construido com o SDK, e com que versao.
func TestSeloDoSDKSaiNoNo(t *testing.T) {
	g := grafoComEtapas(t, map[string]postgres.EstadoNo{
		"b": {NodeID: "b", Status: "running", Etapas: quatroEtapas(), SdkVersao: "v0.44.1"},
		"c": {NodeID: "c", Status: "success"},
	})
	for _, n := range g.Nodes {
		switch n.ID {
		case "b":
			if n.Data["sdk"] != "v0.44.1" {
				t.Errorf("o passo do SDK saiu sem selo: %v", n.Data)
			}
		case "c":
			if _, tem := n.Data["sdk"]; tem {
				t.Errorf("um passo que nao se anunciou ganhou selo: %v", n.Data)
			}
		}
	}
}

// Um passo que nao e do SDK continua exatamente como era: sem grupo, sem
// filhos, sem campo novo. Etapa faltando nunca pode mudar a tela de um passo
// que funciona.
func TestPassoComumNaoGanhaCampoNovo(t *testing.T) {
	g := grafoComEtapas(t, map[string]postgres.EstadoNo{
		"a": {NodeID: "a", Status: "success", DuracaoMs: 1200},
	})
	for _, n := range g.Nodes {
		if n.ParentID != "" || n.Extent != "" || n.Style != nil || n.Selectable != nil {
			t.Errorf("no comum ganhou campo de aninhamento: %+v", n)
		}
		if n.Type != "brevis" {
			t.Errorf("no comum mudou de tipo: %q", n.Type)
		}
	}
}
