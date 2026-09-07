package api

import (
	"log/slog"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/pages"
)

func list() []postgres.WorkflowSummary {
	return []postgres.WorkflowSummary{
		{Slug: "id_verification", Cron: "0 4 * * *", Active: true, HasSchedule: true,
			LastStatus: "success", Tags: []string{"acme", "id"}},
		{Slug: "platform_workspace", Cron: "0 5 * * *", Active: true, HasSchedule: true,
			LastStatus: "failed", Tags: []string{"acme", "platform"}},
		{Slug: "vendors_inmet", Cron: "30 6 * * *", Active: false, HasSchedule: true,
			LastStatus: "success", Tags: []string{"vendors"}},
		// No schedule: it never had a cron, and must not show as "paused".
		{Slug: "protect_ad_hoc", HasSchedule: false, LastStatus: ""},
	}
}

func slugs(ws []postgres.WorkflowSummary) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Slug
	}
	return out
}

func equal(t *testing.T, obtido, esperado []string) {
	t.Helper()
	if len(obtido) != len(esperado) {
		t.Fatalf("got %v, want %v", obtido, esperado)
	}
	for i := range obtido {
		if obtido[i] != esperado[i] {
			t.Fatalf("got %v, want %v", obtido, esperado)
		}
	}
}

func TestFiltrar(t *testing.T) {
	casos := []struct {
		nome     string
		f        pages.Filter
		esperado []string
	}{
		{"no filter", pages.Filter{}, []string{"id_verification", "platform_workspace", "vendors_inmet", "protect_ad_hoc"}},
		{"busca parcial", pages.Filter{Search: "verif"}, []string{"id_verification"}},
		{"busca ignora caixa", pages.Filter{Search: "PLATFORM"}, []string{"platform_workspace"}},
		{"ultimo estado", pages.Filter{State: "failed"}, []string{"platform_workspace"}},
		{"ativos", pages.Filter{Active: "active"}, []string{"id_verification", "platform_workspace"}},
		// The case that motivated the test: "paused" is a schedule switched off,
		// not the absence of one. Merging the two would hide the workflow somebody
		// paused.
		{"paused does not include those that never had a schedule", pages.Filter{Active: "paused"}, []string{"vendors_inmet"}},
		{"tag", pages.Filter{Tag: "acme"}, []string{"id_verification", "platform_workspace"}},
		{"combinado", pages.Filter{Tag: "acme", State: "success"}, []string{"id_verification"}},
		{"nada casa", pages.Filter{Search: "inexistente"}, nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			equal(t, slugs(filtrar(list(), c.f)), c.esperado)
		})
	}
}

// The tag list comes from ALL the workflows, not from the already-filtered rows:
// if it shrank on every click, there would be no way back from one filter to
// another.
func TestTheTagsDoNotShrinkWithTheFilter(t *testing.T) {
	todos := list()
	equal(t, tagsDe(todos), []string{"acme", "id", "platform", "vendors"})

	so := filtrar(todos, pages.Filter{Tag: "vendors"})
	if len(so) != 1 {
		t.Fatalf("expected 1 filtered row, got %d", len(so))
	}
	equal(t, tagsDe(todos), []string{"acme", "id", "platform", "vendors"})
}

func TestTheWorkflowsNextRun(t *testing.T) {
	agora := time.Date(2026, 3, 10, 4, 30, 0, 0, time.UTC)

	ativo := postgres.WorkflowSummary{Slug: "a", Cron: "0 5 * * *", Timezone: "UTC", Active: true}
	if p := proximaDoWorkflow(ativo, agora); p == nil || !p.Equal(time.Date(2026, 3, 10, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("next = %v, want 05:00 do mesmo dia", p)
	}

	// Paused has no next firing: showing one would mislead whoever paused it.
	pausado := postgres.WorkflowSummary{Slug: "b", Cron: "0 5 * * *", Active: false}
	if p := proximaDoWorkflow(pausado, agora); p != nil {
		t.Errorf("workflow pausado devolveu next disparo: %v", p)
	}

	semCron := postgres.WorkflowSummary{Slug: "c", Active: true}
	if p := proximaDoWorkflow(semCron, agora); p != nil {
		t.Errorf("a workflow with no cron returned a next firing: %v", p)
	}

	invalido := postgres.WorkflowSummary{Slug: "d", Cron: "this is not a cron", Active: true}
	if p := proximaDoWorkflow(invalido, agora); p != nil {
		t.Errorf("cron invalido devolveu next disparo: %v", p)
	}
}

func TestNextRunsSortsAndIgnoresInvalidOnes(t *testing.T) {
	agora := time.Date(2026, 3, 10, 4, 30, 0, 0, time.UTC)
	agendas := []postgres.ScheduleSummary{
		{WorkflowSlug: "tarde", Cron: "0 22 * * *", Timezone: "UTC", Active: true},
		{WorkflowSlug: "cedo", Cron: "0 5 * * *", Timezone: "UTC", Active: true},
		{WorkflowSlug: "pausada", Cron: "* * * * *", Timezone: "UTC", Active: false},
		// One broken schedule in the database must not take the whole dashboard
		// down.
		{WorkflowSlug: "quebrada", Cron: "@@@", Timezone: "UTC", Active: true},
	}

	out := proximasExecucoes(agendas, agora, 8, slog.New(slog.DiscardHandler))
	if len(out) != 2 {
		t.Fatalf("obtive %d entradas, want 2 (a pausada e a quebrada ficam de fora)", len(out))
	}
	if out[0].Workflow != "cedo" || out[1].Workflow != "tarde" {
		t.Errorf("ordem = %s, %s; want o disparo mais next primeiro", out[0].Workflow, out[1].Workflow)
	}
}

func TestNextRunsRespectsTheLimit(t *testing.T) {
	agora := time.Now()
	var agendas []postgres.ScheduleSummary
	for i := 0; i < 20; i++ {
		agendas = append(agendas, postgres.ScheduleSummary{
			WorkflowSlug: "w", Cron: "0 * * * *", Timezone: "UTC", Active: true,
		})
	}
	if out := proximasExecucoes(agendas, agora, 5, slog.New(slog.DiscardHandler)); len(out) != 5 {
		t.Errorf("obtive %d, want 5", len(out))
	}
}

func withTimes(slug string, ultima *time.Time, proxima *time.Time) postgres.WorkflowSummary {
	return postgres.WorkflowSummary{Slug: slug, LastRunAt: ultima, NextRun: proxima, HasSchedule: true}
}

func TestSortingByLastRun(t *testing.T) {
	t1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	base := func() []postgres.WorkflowSummary {
		return []postgres.WorkflowSummary{
			withTimes("b_recente", &t2, nil),
			withTimes("a_nunca", nil, nil),
			withTimes("c_antiga", &t1, nil),
		}
	}

	asc := base()
	ordenar(asc, pages.Filter{Sort: "last"})
	equal(t, slugs(asc), []string{"c_antiga", "b_recente", "a_nunca"})

	// The absent one goes last in BOTH directions: treating null as "very old"
	// would make the list start with the ones that never ran precisely when
	// looking for the most recent run.
	desc := base()
	ordenar(desc, pages.Filter{Sort: "last", Desc: true})
	equal(t, slugs(desc), []string{"b_recente", "c_antiga", "a_nunca"})
}

func TestSortingByScheduleSendsTheCronlessToTheEnd(t *testing.T) {
	ws := []postgres.WorkflowSummary{
		{Slug: "sem_cron"},
		{Slug: "cinco", Cron: "0 5 * * *"},
		{Slug: "quatro", Cron: "0 4 * * *"},
	}
	ordenar(ws, pages.Filter{Sort: "schedule"})
	equal(t, slugs(ws), []string{"quatro", "cinco", "sem_cron"})
}

// A tie is settled by the slug: without that, two "never ran" rows would swap
// places on every page load.
func TestTheSortIsStable(t *testing.T) {
	ws := []postgres.WorkflowSummary{{Slug: "zulu"}, {Slug: "alfa"}, {Slug: "mike"}}
	ordenar(ws, pages.Filter{Sort: "last"})
	equal(t, slugs(ws), []string{"alfa", "mike", "zulu"})
}

func TestSlicingAPage(t *testing.T) {
	var ws []postgres.WorkflowSummary
	for i := 0; i < 7; i++ {
		ws = append(ws, postgres.WorkflowSummary{Slug: string(rune('a' + i))})
	}
	f := pages.Filter{Page: 2, PorPagina: 3}
	equal(t, slugs(recortar(ws, f)), []string{"d", "e", "f"})

	// A page past the end happens when filtering while on a high page; it has to
	// voltar vazia em vez de estourar o slice.
	if r := recortar(ws, pages.Filter{Page: 9, PorPagina: 3}); r != nil {
		t.Errorf("pagina fora do intervalo devolveu %d linhas", len(r))
	}
	equal(t, slugs(recortar(ws, pages.Filter{Page: 3, PorPagina: 3})), []string{"g"})
}

func TestAValidStateRefusesAnUnknownOne(t *testing.T) {
	if validState("success") != "success" {
		t.Error("a legitimate state was refused")
	}
	// `?state=xpto` would return an empty list with every chip greyed out, and the
	// operator would read that as "there are no runs".
	if validState("xpto") != "" || validState("' OR 1=1") != "" {
		t.Error("an unknown state should become 'no filter'")
	}
}

func TestTheInstantAndThePage(t *testing.T) {
	if instante("") != nil || instante("ontem") != nil {
		t.Error("an invalid value has to become the absence of a filter, not an error")
	}
	if got := instante("2026-09-01T02:00:00Z"); got == nil || got.Hour() != 2 {
		t.Errorf("it did not parse the RFC3339 the chart emits: %v", got)
	}
	for _, s := range []string{"", "0", "-3", "abc"} {
		if page(s) != 1 {
			t.Errorf("pagina(%q) = %d, want 1", s, page(s))
		}
	}
	if page("4") != 4 {
		t.Error("a valid page was ignored")
	}
}
