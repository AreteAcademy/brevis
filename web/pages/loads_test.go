package pages

import (
	"net/url"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// loadsFixture is the catalog fixture (a destination in every status at a
// fixed 11:20) with the fresh step's configuration: an HTTP source.
func loadsFixture() []postgres.CatalogEntry {
	entries := catalogFixture()
	AttachStepConfigs(entries, map[postgres.StepKey]*postgres.WriterConfig{
		{Workflow: "orders", Node: "load", Target: "bigquery://acme/bronze/fresh"}: sourceConfig(
			field("source.driver", "from.HTTP"), field("source.url", "https://api.acme.io/orders")),
	})
	return entries
}

func loadKeys(v LoadsView) string {
	var out []string
	for _, r := range v.Rows {
		out = append(out, r.Workflow+"·"+r.Step)
	}
	return strings.Join(out, " ")
}

// THE LOADS ARE ONE ROW PER STEP AND DESTINATION, WORST FIRST (screen-loads
// T2): the Catalog's order -- stale, late, on time, paused, no schedule --
// and within a status the most recently loaded first. A gateway's stream is
// not a load, and a legacy label waits until it is asked for, as in the
// Catalog (T3).
func TestTheLoadsAreWorstFirst(t *testing.T) {
	v := BuildLoads(loadsFixture(), DataFilter{}, catalogNow)
	if got, want := loadKeys(v), "vendors·fetch billing·sync orders·load profile·run export·write"; got != want {
		t.Errorf("rows =\n %s\nwant\n %s", got, want)
	}
	if v.Total != 5 || v.Workflows != 5 {
		t.Errorf("total %d, workflows %d, want 5 and 5", v.Total, v.Workflows)
	}
	if got := loadKeys(BuildLoads(loadsFixture(), DataFilter{Legacy: true}, catalogNow)); !strings.HasSuffix(got, "payments·load") {
		t.Errorf("asking for legacy rows does not show them: %s", got)
	}
}

// THE FILTERS NARROW THE ROWS (screen-loads T3): attention, one status, the
// origin -- declared or not -- and a search in the workflow, the step, the
// origin and the destination.
func TestTheLoadsFilter(t *testing.T) {
	for query, want := range map[string]string{
		"status=attention":          "vendors·fetch billing·sync",
		"status=paused":             "profile·run",
		"source=http":               "orders·load",
		"source=none":               "vendors·fetch billing·sync profile·run export·write",
		"source=none&status=late":   "billing·sync",
		"q=BILLING":                 "billing·sync",
		"q=fetch":                   "vendors·fetch",
		"q=api.acme.io":             "orders·load",
		"q=analysis":                "profile·run",
		"q=payments&legacy=1":       "payments·load",
		"source=bogus&status=bogus": "vendors·fetch billing·sync orders·load profile·run export·write",
	} {
		q, _ := url.ParseQuery(query)
		if got := loadKeys(BuildLoads(loadsFixture(), ParseDataFilter(q), catalogNow)); got != want {
			t.Errorf("%s draws %q, want %q", query, got, want)
		}
	}
}

// THE BAND COUNTS WHAT EACH CHOICE WOULD SHOW, with the origin and the search
// as they are; the summary counts every load.
func TestTheLoadsBandCountsEachStatus(t *testing.T) {
	v := BuildLoads(loadsFixture(), DataFilter{}, catalogNow)
	for status, want := range map[string]int{"": 5, "attention": 2, "stale": 1, "late": 1, "on_time": 1, "paused": 1, "unscheduled": 1} {
		if got := v.Count(status); got != want {
			t.Errorf("%q counts %d, want %d", status, got, want)
		}
	}
	h := BuildLoads(loadsFixture(), DataFilter{Source: "http", Status: "stale"}, catalogNow)
	if h.Count("attention") != 0 || h.Count("on_time") != 1 || h.Total != 5 {
		t.Errorf("under source=http: attention %d, on time %d, total %d; want 0, 1, 5", h.Count("attention"), h.Count("on_time"), h.Total)
	}
	if got := strings.Join(v.Sources, " "); got != "http none" {
		t.Errorf("origins offered = %q, want the ones present", got)
	}
}

// A ROW SAYS WHO, FROM WHERE, TO WHERE AND HOW IT GOES: the workflow and the
// step with its schedule in words, the declared origin -- or that none was
// declared -- then the destination, the status, the last load, the rows and
// the last loads; and it opens the load.
func TestALoadRowSaysWhoFromWhereAndToWhere(t *testing.T) {
	page := drawIn(t, "en", LoadsList(BuildLoads(loadsFixture(), DataFilter{Path: "/ingest/loads", Legacy: true}, catalogNow)))
	for _, want := range []string{
		"orders", "load", "hourly at :00", // who, and when
		`aria-label="HTTP"`, "https://api.acme.io/orders", // a declared origin
		"not declared", "Only the Go SDK describes", // an undeclared one, and why
		">bigquery://acme/bronze/<", ">fresh<", // the destination, where then what
		"stale", "late", "on time",
		`data-href="/ingest/loads/orders/load"`, `href="/workflows/orders"`,
		"<svg", // the last loads
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the list has no %q", want)
		}
	}
	if strings.Contains(page, "pubsub://acme/clicks") {
		t.Error("a gateway's stream is listed as a load")
	}
	// The legacy label has no scheme: no kind is made up for it.
	legacy := page[strings.Index(page, `data-href="/ingest/loads/payments/load"`):]
	legacy = legacy[:strings.Index(legacy, "</tr>")]
	if strings.Contains(legacy, "Local files") || !strings.Contains(legacy, "legacy") {
		t.Errorf("the legacy row names a kind it has not: %s", legacy)
	}
}

// THE SUMMARY COUNTS AND LEADS TO THE CATALOG: destinations, workflows, each
// status present, and "see in the catalog".
func TestTheLoadsSummaryCountsAndLeadsToTheCatalog(t *testing.T) {
	page := drawIn(t, "en", Loads(BuildLoads(loadsFixture(), DataFilter{Path: "/ingest/loads"}, catalogNow)))
	summary := page[strings.Index(page, "data-loads-summary"):]
	summary = summary[:strings.Index(summary, "</div>")]
	for _, want := range []string{"5 destinations", "5 workflows", "stale", "late", "on time", "paused", "no schedule", `href="/data"`} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary has no %q", want)
		}
	}
	if !strings.Contains(page, `href="/workflows"`) || !strings.Contains(page, "Writes made outside Brevis do not show") {
		t.Error("no way to the workflows, or no honest footer")
	}
	// Under a status, the catalog opens under the same one (T3).
	late := drawIn(t, "en", LoadsHead(BuildLoads(loadsFixture(), DataFilter{Path: "/ingest/loads", Status: "late"}, catalogNow)))
	if !strings.Contains(late, `href="/data?status=late"`) {
		t.Error("see in the catalog drops the status")
	}
}

// THE LOADS SPEAK THE READER'S LANGUAGE (screen-loads T2): the title, the
// lead, the summary, the table, the origin, the empty states. Workflows,
// steps, URIs and drivers are the data's own, and "workflow" is the
// product's word in every language. The filters too (T3).
func TestTheLoadsHaveNoEnglishLeft(t *testing.T) {
	v := BuildLoads(loadsFixture(), DataFilter{Path: "/ingest/loads"}, catalogNow)
	v.HasLegacy = true
	text := visibleText(drawIn(t, "pt-BR", Loads(v))) + " " +
		visibleText(drawIn(t, "pt-BR", LoadsList(BuildLoads(nil, DataFilter{Path: "/ingest/loads"}, catalogNow)))) + " " +
		visibleText(drawIn(t, "pt-BR", LoadsList(BuildLoads(loadsFixture(), DataFilter{Path: "/ingest/loads", Q: "zzz"}, catalogNow))))
	for _, english := range []string{
		"All", "Needs attention", "Any origin", "Show legacy", "Search", "clear filters",
		"Batch loads", "Extractions", "See workflows", "destinations", "see in the catalog",
		"stale", "late", "on time", "paused", "no schedule", "hourly", "by hand",
		"Origin", "Status", "Last load", "Rows", "Last 12", "not declared",
		"No load yet", "Nothing matches", "Writes made outside", "min ago", "h ago",
	} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese loads still say %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}
