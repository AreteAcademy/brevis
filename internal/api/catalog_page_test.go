package api_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// catalogEntries are destinations whose status does not hang on the minute:
// an hourly writer silent for five hours is stale whatever the clock says.
func catalogEntries() []postgres.CatalogEntry {
	now := time.Now()
	return []postgres.CatalogEntry{
		{Target: "bigquery://acme/bronze/old", Kind: "bigquery", Writers: []postgres.CatalogWriter{{Workflow: "vendors", Node: "fetch",
			LastLoaded: now.Add(-5 * time.Hour), LastRows: i64(12), HasSchedule: true, Cron: "0 * * * *", Timezone: "UTC", Active: true}},
			Recent: []*int64{i64(10), nil, i64(12)}},
		{Target: "pubsub://acme/clicks", Kind: "pubsub", Writers: []postgres.CatalogWriter{{
			Gateway: &postgres.GatewayWriter{Name: "edge", Stream: "/v1/clicks", Role: "sink"}, LastLoaded: now.Add(-time.Minute)}}},
		{Target: "gs://lake/raw/manual/", Kind: "gs", Writers: []postgres.CatalogWriter{{Workflow: "export", Node: "write", LastLoaded: now.Add(-2 * time.Hour)}}},
		{Target: "bronze.payments", Legacy: true, Writers: []postgres.CatalogWriter{{Workflow: "payments", Node: "load", LastLoaded: now.Add(-48 * time.Hour)}}},
	}
}

var catalogRowRE = regexp.MustCompile(`<tr data-href="/data/target\?u=([^"]+)"`)

// catalogRows is the targets the catalog draws, in order.
func catalogRows(body string) []string {
	var out []string
	for _, m := range catalogRowRE.FindAllStringSubmatch(body, -1) {
		u, _ := url.QueryUnescape(m[1])
		out = append(out, u)
	}
	return out
}

// THE CATALOG IS BACK AT /data (screen-catalog T1): every destination, worst
// first, each row opening the destination; a legacy row only when asked for.
func TestTheCatalogIsBackAtData(t *testing.T) {
	ui := dataUI(catalogFake{entries: catalogEntries()})
	code, body := get(t, ui, "/data")
	if code != http.StatusOK {
		t.Fatalf("/data answered %d", code)
	}
	for _, want := range []string{">Data catalog<", "what each writer", "Writes made outside Brevis do not show"} {
		if !strings.Contains(body, want) {
			t.Errorf("the catalog has no %q", want)
		}
	}
	if got := strings.Join(catalogRows(body), " "); got != "bigquery://acme/bronze/old pubsub://acme/clicks gs://lake/raw/manual/" {
		t.Errorf("rows = %q", got)
	}
	_, body = get(t, ui, "/data?legacy=1")
	if !strings.Contains(strings.Join(catalogRows(body), " "), "bronze.payments") {
		t.Error("Show legacy does not show the legacy row")
	}
}

// THE BAND FILTERS BY ITS LINKS, each choice with its count and its rule;
// Needs attention is stale and late together.
func TestTheCatalogsBandFilters(t *testing.T) {
	ui := dataUI(catalogFake{entries: catalogEntries()})
	_, body := get(t, ui, "/data")
	at := strings.Index(body, "data-status-band")
	if at < 0 {
		t.Fatal("there is no status band")
	}
	band := body[at : at+strings.Index(body[at:], "</nav>")]
	for _, want := range []string{
		`href="/data?status=attention"`, ">Needs attention<", `href="/data?status=continuous"`, "data-count=\"1\"",
		`title="A writer missed two of its slots`,
	} {
		if !strings.Contains(band, want) {
			t.Errorf("the band has no %s", want)
		}
	}
	_, body = get(t, ui, "/data?status=attention")
	if got := strings.Join(catalogRows(body), " "); got != "bigquery://acme/bronze/old" {
		t.Errorf("attention draws %q", got)
	}
}

// A ROW SAYS WHAT AND WHO: where, faint, then the table; its status; who
// writes it; when it last loaded; how many rows; and its last loads.
func TestACatalogRowSaysWhatAndWho(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: catalogEntries()}), "/data")
	at := strings.Index(body, `<tr data-href="/data/target?u=bigquery%3A%2F%2Facme%2Fbronze%2Fold"`)
	if at < 0 {
		t.Fatal("no row for the stale table")
	}
	row := body[at : at+strings.Index(body[at:], "</tr>")]
	for _, want := range []string{"bigquery://acme/bronze/", ">old<", "stale", "vendors · fetch", ">12<", "data-recent"} {
		if !strings.Contains(row, want) {
			t.Errorf("the row has no %s", want)
		}
	}
}

// THE CATALOG ANSWERS WHILE SOMEBODY TYPES, and an empty answer says how to
// get out of it.
func TestTheCatalogAnswersTheSearch(t *testing.T) {
	ui := dataUI(catalogFake{entries: catalogEntries()})
	code, body := answerGet(t, ui, "/data?q=clicks")
	if code != http.StatusOK || strings.Contains(body, "<html") {
		t.Fatalf("the fragment answered %d with the page", code)
	}
	if got := strings.Join(catalogRows(body), " "); got != "pubsub://acme/clicks" {
		t.Errorf("q=clicks draws %q", got)
	}
	_, body = answerGet(t, ui, "/data?q=nothing")
	if !strings.Contains(body, "No destination matches") || !strings.Contains(body, `href="/data"`) {
		t.Error("an empty search does not say how to get out of it")
	}
}
