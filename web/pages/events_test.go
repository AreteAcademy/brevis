package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	evkind "github.com/AreteAcademy/brevis/internal/domain/events"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// EVERY KIND THE FEED KNOWS IS A SENTENCE, in every language, with a place
// for what it is about -- a kind with no sentence would draw its key.
func TestEveryEventKindIsASentence(t *testing.T) {
	for _, lang := range []string{"en", "pt-BR", "es"} {
		ctx := i18n.WithLang(context.Background(), lang)
		for _, k := range evkind.Kinds {
			before, after := eventSentence(ctx, k)
			if strings.Contains(before+after, "[") || before+after == "" {
				t.Errorf("%s: %s reads %q · %q", lang, k, before, after)
			}
		}
	}
}

func eventsTab(t *testing.T, evs []postgres.Event) string {
	t.Helper()
	page := draw(t, Overview(OverviewData{Window: ParseWindow(""), Now: time.Now(), Events: evs}))
	at := strings.Index(page, `id="events" data-tab-panel="events"`)
	if at < 0 {
		t.Fatal("no events tab")
	}
	return page[at : at+strings.Index(page[at:], "</section>")]
}

// THE EVENT LOG: the time, where it comes from, what happened with its
// subject as code, who did it, and the way to the run; a kind this version
// does not know -- written by a newer one -- is skipped, not guessed.
func TestTheEventLog(t *testing.T) {
	run := uuid.MustParse("8d36ef6e-0000-0000-0000-000000000001")
	tab := eventsTab(t, []postgres.Event{
		{At: time.Now(), Kind: evkind.RunFailed, Subject: "daily_sales", RunID: &run},
		{At: time.Now(), Kind: evkind.WorkflowPublished, Subject: "daily_sales", Actor: "ana"},
		{At: time.Now(), Kind: evkind.Kind("thing.from_the_future"), Subject: "x"},
	})
	for _, want := range []string{
		`<code class="font-mono text-[12px] text-ink">daily_sales</code>`, "failed", `href="/runs/` + run.String() + `"`,
		"published", "by ana", "Workflow",
	} {
		if !strings.Contains(tab, want) {
			t.Errorf("the event log lacks %s", want)
		}
	}
	if n := strings.Count(tab, "data-event="); n != 2 {
		t.Errorf("%d events drawn; wanted 2, the unknown one skipped", n)
	}
}

// RECENT RUNS AND THE EVENT LOG ARE TABS, by the hash; the runs filter on the
// page -- every row says its state -- and stack with no script.
func TestTheOverviewsTabs(t *testing.T) {
	page := draw(t, Overview(OverviewData{Window: ParseWindow(""), Now: time.Now(), Recentes: []postgres.RunSummary{
		{ID: "a", WorkflowSlug: "w", Status: "failed", CreatedAt: time.Now()},
	}}))
	for _, want := range []string{
		`id="overview-tabs" data-hash-tabs`, `data-tab="recent"`, `data-tab="events"`,
		`id="recent" data-tab-panel="recent"`, `#overview-tabs [data-tab-panel]{display:none}`,
		`data-recent-filter="all"`, `data-recent-filter="failed"`, `data-recent-filter="running"`,
		`data-run-id="a" data-status="failed"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no %s", want)
		}
	}
}
