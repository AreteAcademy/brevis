package api

import (
	"log/slog"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/pages"
)

func lista() []postgres.WorkflowSummary {
	return []postgres.WorkflowSummary{
		{Slug: "id_verification", Cron: "0 4 * * *", Active: true, TemAgenda: true,
			UltimoStatus: "success", Tags: []string{"acme", "id"}},
		{Slug: "platform_workspace", Cron: "0 5 * * *", Active: true, TemAgenda: true,
			UltimoStatus: "failed", Tags: []string{"acme", "platform"}},
		{Slug: "vendors_inmet", Cron: "30 6 * * *", Active: false, TemAgenda: true,
			UltimoStatus: "success", Tags: []string{"vendors"}},
		// Sem agenda: nunca teve cron, e nao deve aparecer como "pausado".
		{Slug: "protect_ad_hoc", TemAgenda: false, UltimoStatus: ""},
	}
}

func slugs(ws []postgres.WorkflowSummary) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Slug
	}
	return out
}

func igual(t *testing.T, obtido, esperado []string) {
	t.Helper()
	if len(obtido) != len(esperado) {
		t.Fatalf("obtido %v, quero %v", obtido, esperado)
	}
	for i := range obtido {
		if obtido[i] != esperado[i] {
			t.Fatalf("obtido %v, quero %v", obtido, esperado)
		}
	}
}

func TestFiltrar(t *testing.T) {
	casos := []struct {
		nome     string
		f        pages.Filter
		esperado []string
	}{
		{"sem filtro", pages.Filter{}, []string{"id_verification", "platform_workspace", "vendors_inmet", "protect_ad_hoc"}},
		{"busca parcial", pages.Filter{Busca: "verif"}, []string{"id_verification"}},
		{"busca ignora caixa", pages.Filter{Busca: "PLATFORM"}, []string{"platform_workspace"}},
		{"ultimo estado", pages.Filter{State: "failed"}, []string{"platform_workspace"}},
		{"ativos", pages.Filter{Active: "active"}, []string{"id_verification", "platform_workspace"}},
		// O caso que motivou o teste: "pausado" e agenda desligada, nao ausencia
		// de agenda. Juntar os dois esconderia o workflow que alguem pausou.
		{"pausados nao incluem quem nunca teve agenda", pages.Filter{Active: "paused"}, []string{"vendors_inmet"}},
		{"tag", pages.Filter{Tag: "acme"}, []string{"id_verification", "platform_workspace"}},
		{"combinado", pages.Filter{Tag: "acme", State: "success"}, []string{"id_verification"}},
		{"nada casa", pages.Filter{Busca: "inexistente"}, nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			igual(t, slugs(filtrar(lista(), c.f)), c.esperado)
		})
	}
}

// A lista de tags vem de TODOS os workflows, nao das linhas ja filtradas: se
// encolhesse a cada clique, nao haveria como voltar de um filtro para outro.
func TestTagsDeNaoEncolhemComOFiltro(t *testing.T) {
	todos := lista()
	igual(t, tagsDe(todos), []string{"acme", "id", "platform", "vendors"})

	so := filtrar(todos, pages.Filter{Tag: "vendors"})
	if len(so) != 1 {
		t.Fatalf("esperava 1 linha filtrada, veio %d", len(so))
	}
	igual(t, tagsDe(todos), []string{"acme", "id", "platform", "vendors"})
}

func TestProximaDoWorkflow(t *testing.T) {
	agora := time.Date(2026, 3, 10, 4, 30, 0, 0, time.UTC)

	ativo := postgres.WorkflowSummary{Slug: "a", Cron: "0 5 * * *", Timezone: "UTC", Active: true}
	if p := proximaDoWorkflow(ativo, agora); p == nil || !p.Equal(time.Date(2026, 3, 10, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("proximo = %v, quero 05:00 do mesmo dia", p)
	}

	// Pausado nao tem proximo disparo: mostrar um enganaria quem pausou.
	pausado := postgres.WorkflowSummary{Slug: "b", Cron: "0 5 * * *", Active: false}
	if p := proximaDoWorkflow(pausado, agora); p != nil {
		t.Errorf("workflow pausado devolveu proximo disparo: %v", p)
	}

	semCron := postgres.WorkflowSummary{Slug: "c", Active: true}
	if p := proximaDoWorkflow(semCron, agora); p != nil {
		t.Errorf("workflow sem cron devolveu proximo disparo: %v", p)
	}

	invalido := postgres.WorkflowSummary{Slug: "d", Cron: "isto nao e cron", Active: true}
	if p := proximaDoWorkflow(invalido, agora); p != nil {
		t.Errorf("cron invalido devolveu proximo disparo: %v", p)
	}
}

func TestProximasExecucoesOrdenaEIgnoraInvalidas(t *testing.T) {
	agora := time.Date(2026, 3, 10, 4, 30, 0, 0, time.UTC)
	agendas := []postgres.ScheduleSummary{
		{WorkflowSlug: "tarde", Cron: "0 22 * * *", Timezone: "UTC", Active: true},
		{WorkflowSlug: "cedo", Cron: "0 5 * * *", Timezone: "UTC", Active: true},
		{WorkflowSlug: "pausada", Cron: "* * * * *", Timezone: "UTC", Active: false},
		// Uma agenda quebrada no banco nao pode derrubar o dashboard inteiro.
		{WorkflowSlug: "quebrada", Cron: "@@@", Timezone: "UTC", Active: true},
	}

	out := proximasExecucoes(agendas, agora, 8, slog.New(slog.DiscardHandler))
	if len(out) != 2 {
		t.Fatalf("obtive %d entradas, quero 2 (a pausada e a quebrada ficam de fora)", len(out))
	}
	if out[0].Workflow != "cedo" || out[1].Workflow != "tarde" {
		t.Errorf("ordem = %s, %s; quero o disparo mais proximo primeiro", out[0].Workflow, out[1].Workflow)
	}
}

func TestProximasExecucoesRespeitaLimite(t *testing.T) {
	agora := time.Now()
	var agendas []postgres.ScheduleSummary
	for i := 0; i < 20; i++ {
		agendas = append(agendas, postgres.ScheduleSummary{
			WorkflowSlug: "w", Cron: "0 * * * *", Timezone: "UTC", Active: true,
		})
	}
	if out := proximasExecucoes(agendas, agora, 5, slog.New(slog.DiscardHandler)); len(out) != 5 {
		t.Errorf("obtive %d, quero 5", len(out))
	}
}

func comTempo(slug string, ultima *time.Time, proxima *time.Time) postgres.WorkflowSummary {
	return postgres.WorkflowSummary{Slug: slug, UltimaRunEm: ultima, ProximaRun: proxima, TemAgenda: true}
}

func TestOrdenarPorUltimaExecucao(t *testing.T) {
	t1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	base := func() []postgres.WorkflowSummary {
		return []postgres.WorkflowSummary{
			comTempo("b_recente", &t2, nil),
			comTempo("a_nunca", nil, nil),
			comTempo("c_antiga", &t1, nil),
		}
	}

	asc := base()
	ordenar(asc, pages.Filter{Sort: "last"})
	igual(t, slugs(asc), []string{"c_antiga", "b_recente", "a_nunca"})

	// O ausente fica por ultimo NAS DUAS direcoes: tratar nulo como "muito
	// antigo" faria a lista comecar por quem nunca rodou justamente ao procurar
	// a execucao mais recente.
	desc := base()
	ordenar(desc, pages.Filter{Sort: "last", Desc: true})
	igual(t, slugs(desc), []string{"b_recente", "c_antiga", "a_nunca"})
}

func TestOrdenarPorAgendaMandaSemCronParaOFim(t *testing.T) {
	ws := []postgres.WorkflowSummary{
		{Slug: "sem_cron"},
		{Slug: "cinco", Cron: "0 5 * * *"},
		{Slug: "quatro", Cron: "0 4 * * *"},
	}
	ordenar(ws, pages.Filter{Sort: "schedule"})
	igual(t, slugs(ws), []string{"quatro", "cinco", "sem_cron"})
}

// Empate resolvido pelo slug: sem isso, duas linhas "nunca rodou" trocariam de
// lugar a cada carregamento da pagina.
func TestOrdenacaoEhEstavel(t *testing.T) {
	ws := []postgres.WorkflowSummary{{Slug: "zulu"}, {Slug: "alfa"}, {Slug: "mike"}}
	ordenar(ws, pages.Filter{Sort: "last"})
	igual(t, slugs(ws), []string{"alfa", "mike", "zulu"})
}

func TestRecortarPagina(t *testing.T) {
	var ws []postgres.WorkflowSummary
	for i := 0; i < 7; i++ {
		ws = append(ws, postgres.WorkflowSummary{Slug: string(rune('a' + i))})
	}
	f := pages.Filter{Page: 2, PorPagina: 3}
	igual(t, slugs(recortar(ws, f)), []string{"d", "e", "f"})

	// Pagina alem do fim acontece ao filtrar estando numa pagina alta; tem de
	// voltar vazia em vez de estourar o slice.
	if r := recortar(ws, pages.Filter{Page: 9, PorPagina: 3}); r != nil {
		t.Errorf("pagina fora do intervalo devolveu %d linhas", len(r))
	}
	igual(t, slugs(recortar(ws, pages.Filter{Page: 3, PorPagina: 3})), []string{"g"})
}

func TestEstadoValidoRecusaDesconhecido(t *testing.T) {
	if estadoValido("success") != "success" {
		t.Error("estado legitimo foi recusado")
	}
	// `?estado=xpto` devolveria lista vazia com todos os chips apagados, e o
	// operador leria isso como "nao ha execucoes".
	if estadoValido("xpto") != "" || estadoValido("' OR 1=1") != "" {
		t.Error("estado desconhecido deveria virar 'sem filtro'")
	}
}

func TestInstanteEPagina(t *testing.T) {
	if instante("") != nil || instante("ontem") != nil {
		t.Error("valor invalido deve virar ausencia de filtro, nao erro")
	}
	if got := instante("2026-09-01T02:00:00Z"); got == nil || got.Hour() != 2 {
		t.Errorf("nao interpretou o RFC3339 que o grafico emite: %v", got)
	}
	for _, s := range []string{"", "0", "-3", "abc"} {
		if pagina(s) != 1 {
			t.Errorf("pagina(%q) = %d, quero 1", s, pagina(s))
		}
	}
	if pagina("4") != 4 {
		t.Error("pagina valida foi ignorada")
	}
}
