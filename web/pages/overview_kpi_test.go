package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func kpis(t *testing.T, d OverviewData) string {
	t.Helper()
	page := draw(t, Overview(d))
	at := strings.Index(page, "data-kpis")
	if at < 0 {
		t.Fatal("no KPIs")
	}
	return page[at : at+strings.Index(page[at:], "</dl>")]
}

// FOUR NUMBERS (screen-overview T4): how much passed, what failed and how
// many workflows still are failing, what runs now and waits, what fires next.
// The retries the strip had are kept, under the success rate.
func TestTheOverviewsFourNumbers(t *testing.T) {
	now := time.Date(2026, 10, 17, 3, 48, 0, 0, time.UTC)
	strip := kpis(t, OverviewData{
		Ind:       postgres.Indicators{Total: 1770, Succeeded: 1742, Failed: 26, Running: 1, Pending: 0, Retried: 4, MeanDuration: 48 * time.Second},
		Pending:   3,
		Window:    ParseWindow(""),
		Now:       now,
		Proximas:  []NextRun{{Workflow: "vendors_fire_meteosat", Cron: "*/5 * * * *", When: now.Add(30 * time.Second)}},
		Attention: Attention{Items: []AttentionItem{{Workflow: "a"}, {Workflow: "b"}}},
	})
	for _, want := range []string{
		"Success rate", "98.5%", "1,742 of 1,768", "4 needed a retry", "last 24 hours",
		"Failures", ">26<", "1.5%", `href="/runs?state=failed"`, "2 workflows still failing",
		"Running now", ">1<", "3 queued", "average duration 48.0s",
		"Next trigger", "in &lt;1m", "vendors_fire_meteosat", "text-state-success",
	} {
		if !strings.Contains(strip, want) {
			t.Errorf("the KPIs lack %s", want)
		}
	}
	if n := strings.Count(strip, "bg-surface px-3.5"); n != 4 {
		t.Errorf("%d cells; wanted 4", n)
	}
}

// Nothing finished is not 0% -- it is nothing to say -- and no failing
// workflow is no link to them.
func TestTheNumbersWithNothingYet(t *testing.T) {
	strip := kpis(t, OverviewData{Window: ParseWindow(""), Now: time.Now()})
	if strings.Contains(strip, "0.0%") || !strings.Contains(strip, "—") {
		t.Error("nothing finished reads as a rate")
	}
	if strings.Contains(strip, "still failing") || !strings.Contains(strip, "no schedule") {
		t.Error("an empty overview offers a link to no failure, or a next trigger")
	}
}

// WHAT FIRES NEXT, AS A READER SAYS IT: what the workflow runs in, when --
// green when it is about to -- the schedule in words and the cron, and the
// way to all the workflows.
func TestTheNextTriggersSayWhenInWords(t *testing.T) {
	now := time.Date(2026, 10, 17, 3, 48, 0, 0, time.UTC)
	page := draw(t, Overview(OverviewData{Window: ParseWindow(""), Now: now, Proximas: []NextRun{
		{Workflow: "vendors_fire_meteosat", Cron: "*/5 * * * *", When: now.Add(40 * time.Second), Logos: []string{"python"}},
		{Workflow: "id_verification", Cron: "0 4 * * *", When: now.Add(12 * time.Minute)},
	}}))
	next := page[strings.Index(page, "data-next-triggers"):]
	next = next[:strings.Index(next, "</section>")]
	for _, want := range []string{
		`aria-label="Python"`, "vendors_fire_meteosat", "in &lt;1m", "text-state-success", "*/5 * * * *",
		CronText("0 4 * * *"), "in 12m", `href="/workflows"`, "All workflows",
	} {
		if !strings.Contains(next, want) {
			t.Errorf("the next triggers lack %s", want)
		}
	}
	if strings.Count(next, "text-state-success") != 1 {
		t.Error("a trigger twelve minutes away is green")
	}
	if strings.Contains(page, "Count and average duration") {
		t.Error("the chart still explains a duration it no longer draws")
	}
}
