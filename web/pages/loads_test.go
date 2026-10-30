package pages

import (
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
// not a load.
func TestTheLoadsAreWorstFirst(t *testing.T) {
	v := BuildLoads(loadsFixture(), catalogNow)
	if got, want := loadKeys(v), "vendors·fetch billing·sync orders·load profile·run export·write payments·load"; got != want {
		t.Errorf("rows =\n %s\nwant\n %s", got, want)
	}
	if v.Total != 6 || v.Workflows != 6 {
		t.Errorf("total %d, workflows %d, want 6 and 6", v.Total, v.Workflows)
	}
}

// A ROW SAYS WHO, FROM WHERE, TO WHERE AND HOW IT GOES: the workflow and the
// step with its schedule in words, the declared origin -- or that none was
// declared -- then the destination, the status, the last load, the rows and
// the last loads; and it opens the load.
func TestALoadRowSaysWhoFromWhereAndToWhere(t *testing.T) {
	page := drawIn(t, "en", LoadsList(BuildLoads(loadsFixture(), catalogNow).Apply(DataFilter{Path: "/ingest/loads"})))
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
	page := drawIn(t, "en", Loads(BuildLoads(loadsFixture(), catalogNow).Apply(DataFilter{Path: "/ingest/loads"})))
	summary := page[strings.Index(page, "data-loads-summary"):]
	summary = summary[:strings.Index(summary, "</div>")]
	for _, want := range []string{"6 destinations", "6 workflows", "stale", "late", "on time", "paused", "no schedule", `href="/data"`} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary has no %q", want)
		}
	}
	if !strings.Contains(page, `href="/workflows"`) || !strings.Contains(page, "Writes made outside Brevis do not show") {
		t.Error("no way to the workflows, or no honest footer")
	}
}

// THE LOADS SPEAK THE READER'S LANGUAGE (screen-loads T2): the title, the
// lead, the summary, the table, the origin, the empty states. Workflows,
// steps, URIs and drivers are the data's own, and "workflow" is the
// product's word in every language. The filter bar is T3's.
func TestTheLoadsHaveNoEnglishLeft(t *testing.T) {
	v := BuildLoads(loadsFixture(), catalogNow).Apply(DataFilter{Path: "/ingest/loads"})
	text := visibleText(drawIn(t, "pt-BR", LoadsList(v))) + " " +
		visibleText(drawIn(t, "pt-BR", LoadsHead(v))) + " " +
		visibleText(drawIn(t, "pt-BR", LoadsList(BuildLoads(nil, catalogNow).Apply(DataFilter{Path: "/ingest/loads"})))) + " " +
		visibleText(drawIn(t, "pt-BR", LoadsList(BuildLoads(loadsFixture(), catalogNow).Apply(DataFilter{Path: "/ingest/loads", Q: "zzz"}))))
	for _, english := range []string{
		"Batch loads", "Extractions", "See workflows", "destinations", "see in the catalog",
		"stale", "late", "on time", "paused", "no schedule", "hourly", "by hand",
		"Origin", "Status", "Last load", "Rows", "Last 12", "not declared",
		"No load yet", "Nothing matches", "Writes made outside", "ago",
	} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese loads still say %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}
