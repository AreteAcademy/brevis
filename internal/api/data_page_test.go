package api_test

import (
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

type catalogFake struct {
	entries []postgres.CatalogEntry
	err     error

	detail *postgres.TargetDetail
	asked  *string
}

func (c catalogFake) Catalog(context.Context) ([]postgres.CatalogEntry, error) {
	return c.entries, c.err
}

func (c catalogFake) CatalogTarget(_ context.Context, target string) (*postgres.TargetDetail, error) {
	if c.asked != nil {
		*c.asked = target
	}
	if c.detail == nil || c.detail.Target != target {
		return nil, nil
	}
	return c.detail, nil
}

func dataUI(c api.CatalogReader) *api.UI {
	return api.NewUI(nil, nil, nil, nil, nil, c, branding.Default(), slog.New(slog.DiscardHandler))
}

func get(t *testing.T, ui *api.UI, path string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func i64(v int64) *int64 { return &v }

func demoCatalog() []postgres.CatalogEntry {
	now := time.Now()
	return []postgres.CatalogEntry{
		{
			Target: "postgres://brevis_it/public/landing_orders", Kind: "postgres",
			Writers: []postgres.CatalogWriter{{Workflow: "sdk_orders", Node: "load",
				LastLoaded: now.Add(-time.Minute), LastRows: i64(37),
				HasSchedule: true, Cron: "* * * * *", Timezone: "UTC", Active: true}},
			Recent: []*int64{i64(0), i64(37)},
		},
		{
			Target: "s3://demo-landing/reports/", Kind: "s3",
			Writers: []postgres.CatalogWriter{{Workflow: "shell_report", Node: "build_report",
				LastLoaded: now.Add(-2 * time.Minute)}},
			Recent: []*int64{nil},
		},
		{
			Target: "bronze.payments", Legacy: true,
			Writers: []postgres.CatalogWriter{{Workflow: "payments", Node: "load", LastLoaded: now.Add(-48 * time.Hour), LastRows: i64(10)}},
		},
	}
}

func TestTheDataPageListsEveryDestinationAndItsWriter(t *testing.T) {
	code, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/data")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		"postgres://brevis_it/public/landing_orders", "sdk_orders", "load",
		"s3://demo-landing/reports/", "shell_report",
		"bronze.payments", "legacy",
		// What is listed, and what is not, is said under the title.
		"Writes from outside Brevis do not appear here",
	} {
		if !strings.Contains(visible(body), want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	// Absent rows are not drawn as zero.
	if strings.Contains(body, ">0 rows<") {
		t.Error("an absent count was rendered as zero")
	}
}

func TestTheDataPageSaysHowADestinationAppears(t *testing.T) {
	code, body := get(t, dataUI(catalogFake{}), "/data")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "No destination yet") || !strings.Contains(body, "landed") {
		t.Errorf("the empty state does not say how a destination appears:\n%s", body)
	}
}

func TestDataIsInTheNavigation(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{}), "/data")
	if !strings.Contains(body, `href="/data"`) {
		t.Error("the navigation has no link to /data")
	}
}

// Every destination carries its status as words, not colour alone, and the
// summary above the table counts them.
func TestTheDataPageShowsStatusesInWordsAndCountsThem(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/data")
	for _, want := range []string{"on time", "no schedule", "3 destinations"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
}

func TestALateWriterLinksToTheRunThatIsAboutToFixIt(t *testing.T) {
	run := "7aa2549c-0503-5f93-ab58-0ab78e7eb555"
	late := []postgres.CatalogEntry{{
		Target: "postgres://db/public/orders", Kind: "postgres",
		Writers: []postgres.CatalogWriter{{Workflow: "orders_sync", Node: "load",
			LastLoaded: time.Now().Add(-90 * time.Minute), HasSchedule: true,
			Cron: "0 * * * *", Timezone: "UTC", Active: true, RunInFlight: &run}},
	}}
	_, body := get(t, dataUI(catalogFake{entries: late}), "/data")
	if !strings.Contains(body, `href="/runs/`+run+`"`) || !strings.Contains(body, "run in progress") {
		t.Error("a late writer with a run in flight does not link to it")
	}
}

func TestTheDataPageFiltersFromTheQueryString(t *testing.T) {
	ui := dataUI(catalogFake{entries: demoCatalog()})

	_, body := get(t, ui, "/data?kind=s3")
	if !strings.Contains(body, "demo-landing") || strings.Contains(body, "landing_orders") {
		t.Error("?kind=s3 did not narrow the table to the s3 destination")
	}
	if !strings.Contains(body, "Showing 1 of 3 destinations") {
		t.Error("a filtered page does not say how much it shows")
	}

	_, body = get(t, ui, "/data?status=attention")
	if !strings.Contains(body, "Nothing matches this filter") {
		t.Error("an empty filter result does not say so")
	}

	_, body = get(t, ui, "/data?status=bogus")
	if !strings.Contains(body, "landing_orders") || strings.Contains(body, "Showing ") {
		t.Error("an unknown status should be ignored, showing everything")
	}
}

func lateDetail() *postgres.TargetDetail {
	run := "4b1f0a52-61c3-4f45-8f0e-6d0c2b2f0d11"
	return &postgres.TargetDetail{
		CatalogEntry: postgres.CatalogEntry{
			Target: "postgres://analytics/public/order%20items", Kind: "postgres",
			Writers: []postgres.CatalogWriter{{Workflow: "orders_sync", Node: "load",
				LastLoaded: time.Now().Add(-90 * time.Minute), LastRows: i64(30),
				HasSchedule: true, Cron: "0 * * * *", Timezone: "UTC", Active: true}},
			Recent: []*int64{i64(10), i64(20), i64(30)},
		},
		Loads: []postgres.TargetLoad{
			{RunID: run, Workflow: "orders_sync", Node: "load", LoadedAt: time.Now().Add(-90 * time.Minute), Rows: i64(30)},
			{RunID: "older", Workflow: "orders_sync", Node: "load", LoadedAt: time.Now().Add(-150 * time.Minute), Rows: i64(20)},
		},
	}
}

func TestTheDestinationPageShowsItsWritersAndLoads(t *testing.T) {
	d := lateDetail()
	var asked string
	ui := dataUI(catalogFake{detail: d, asked: &asked})

	code, body := get(t, ui, "/data/target?u="+url.QueryEscape(d.Target))
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	// The target round-trips untouched: a slash, and a percent-encoded space.
	if asked != d.Target {
		t.Fatalf("the reader was asked for %q, want %q", asked, d.Target)
	}
	for _, want := range []string{
		d.Target, "orders_sync", "0 * * * *",
		`href="/runs/` + d.Loads[0].RunID + `"`,
		"missed its", // a late writer says which slot and how much grace
		"Rows per load",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
}

func TestTheDestinationPageRefusesWhatItCannotRead(t *testing.T) {
	ui := dataUI(catalogFake{detail: lateDetail()})
	if code, _ := get(t, ui, "/data/target"); code != http.StatusBadRequest {
		t.Errorf("no target: %d, want 400", code)
	}
	if code, _ := get(t, ui, "/data/target?u="+url.QueryEscape(strings.Repeat("x", 513))); code != http.StatusBadRequest {
		t.Errorf("an oversized target: %d, want 400", code)
	}
	if code, _ := get(t, ui, "/data/target?u="+url.QueryEscape("postgres://nowhere/public/x")); code != http.StatusNotFound {
		t.Errorf("an unknown target: %d, want 404", code)
	}
}

func TestTheListLinksEachDestinationToItsPage(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/data")
	if !strings.Contains(body, `href="/data/target?u=`+url.QueryEscape("s3://demo-landing/reports/")+`"`) {
		t.Error("the list does not link to the destination page")
	}
}

func gatewayCatalog() []postgres.CatalogEntry {
	gw := func(stream, role, note string) postgres.CatalogWriter {
		return postgres.CatalogWriter{Gateway: &postgres.GatewayWriter{Name: "edge", Stream: stream,
			Role: role, Kind: "files", Note: note, PublishedAt: time.Now().Add(-time.Hour)}}
	}
	return []postgres.CatalogEntry{
		{Target: "pubsub://brevis-local/clicks", Kind: "pubsub", Writers: []postgres.CatalogWriter{gw("/v1/clicks", "sink", "")}},
		{Writers: []postgres.CatalogWriter{gw("/v1/clicks", "dead_letter", `"./dead-letter/" is relative to the gateway's working directory`)}},
	}
}

func TestTheDataPageShowsGatewayDestinations(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: gatewayCatalog()}), "/data")
	for _, want := range []string{
		"pubsub://brevis-local/clicks", "continuous", "edge", "/v1/clicks",
		"unidentified", "relative to the gateway", "/metrics",
	} {
		if !strings.Contains(visible(body), want) {
			t.Errorf("the page does not show %q", want)
		}
	}
}

// visible is the page's text as a reader sees it: tags removed and entities
// decoded. A URI drawn as a dimmed prefix and its object is two elements in
// the markup and one string on the screen, and the screen is what these
// tests are about.
func visible(body string) string {
	return html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, ""))
}

func TestADestinationPageNamesTheGatewayThatPublishedIt(t *testing.T) {
	e := gatewayCatalog()[0]
	_, body := get(t, dataUI(catalogFake{detail: &postgres.TargetDetail{CatalogEntry: e}}),
		"/data/target?u="+url.QueryEscape(e.Target))
	if !strings.Contains(body, "Published from gateway") || !strings.Contains(body, "/metrics") {
		t.Errorf("the page does not say which gateway published it:\n%s", body)
	}
}

// THE CATALOG SEARCHES BY WHERE AND BY WHO: a destination's URI, and the
// workflow, step or gateway that writes it -- case-insensitive, because
// nobody remembers whether it was `Orders` or `orders`. On the SERVER, so a
// search is a link and works with no script; ui.js only makes it answer while
// somebody types.
func TestTheCatalogSearchesByWhereAndByWho(t *testing.T) {
	ui := dataUI(catalogFake{entries: demoCatalog()})

	_, body := get(t, ui, "/data?q=REPORTS")
	if rowHidden(t, body, "demo-landing") || !rowHidden(t, body, "landing_orders") || !rowHidden(t, body, "payments") {
		t.Error("?q=REPORTS did not narrow to the destination whose URI says reports")
	}
	if !strings.Contains(body, "Showing 1 of 3 destinations") {
		t.Error("a searched page does not say how much it shows")
	}
	if !strings.Contains(body, `value="REPORTS"`) {
		t.Error("the search box does not keep what was searched")
	}

	_, body = get(t, ui, "/data?q=sdk_orders")
	if rowHidden(t, body, "landing_orders") || !rowHidden(t, body, "demo-landing") {
		t.Error("?q=sdk_orders did not find the destination that workflow writes")
	}

	_, body = get(t, ui, "/data?q=nothing-is-called-this")
	empty := body[strings.LastIndex(body[:strings.Index(body, "Nothing matches this filter")], "<div"):]
	if strings.Contains(empty[:strings.Index(empty, ">")], "hidden") || !strings.Contains(body, `href="/data"`) {
		t.Error("a search that finds nothing does not say so and offer the way back")
	}
}

// A SEARCH DOES NOT DROP THE OTHER FILTERS, AND A FILTER DOES NOT DROP THE
// SEARCH: each is one field of the same URL.
func TestTheSearchAndTheFiltersKeepEachOther(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/data?kind=s3&q=report")
	if !strings.Contains(body, `<input type="hidden" name="kind" value="s3"`) {
		t.Error("searching again would drop the kind filter")
	}
	if !strings.Contains(body, `href="/data?status=on_time&amp;kind=s3&amp;q=report"`) &&
		!strings.Contains(body, `href="/data?kind=s3&amp;q=report"`) {
		t.Error("the filter links drop the search")
	}
}

// A URI IS DRAWN AS WHERE, THEN WHAT. The scheme and host are the same on
// most rows and the eye needs the part that differs, so the prefix is dimmed
// and the object is not -- the whole URI is still there to read and select.
func TestAURIIsDrawnAsWhereThenWhat(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/data")
	for _, want := range []string{">postgres://brevis_it/<", ">public/landing_orders<", ">bronze.<", ">payments<"} {
		if !strings.Contains(body, want) {
			t.Errorf("the URI is not split where it should be: no %s", want)
		}
	}
}

// THE WHOLE ROW GOES TO THE DESTINATION, without taking the writer's own link
// away: the row carries where it goes, ui.js follows it on a click that did
// not land on a link, and the URI stays a real link for the keyboard and for
// no script at all.
func TestTheWholeRowGoesToTheDestination(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/data")
	if !strings.Contains(body, `data-href="/data/target?u=`+url.QueryEscape("s3://demo-landing/reports/")+`"`) {
		t.Error("a row does not say where it goes")
	}
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{`closest("tr[data-href]")`, `closest("a, button, input, label")`} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js never uses %s, so a row is not a link", want)
		}
	}
}

// rowHidden finds the row whose search text holds needle and says whether it
// is drawn hidden. A search on the server HIDES rows rather than dropping
// them: hidden, they are invisible with no script, and the filter in ui.js can
// bring them back as somebody deletes a letter.
func rowHidden(t *testing.T, body, needle string) bool {
	t.Helper()
	at := strings.Index(body, `data-filter-text="`)
	for at >= 0 {
		start := strings.LastIndex(body[:at], "<tr")
		tag := body[start : at+strings.Index(body[at:], ">")]
		if strings.Contains(tag, strings.ToLower(needle)) {
			return strings.Contains(tag, " hidden")
		}
		next := strings.Index(body[at+1:], `data-filter-text="`)
		if next < 0 {
			break
		}
		at += 1 + next
	}
	t.Fatalf("no row's search text holds %q", needle)
	return false
}

// THE LIST FILTERS WHILE SOMEBODY TYPES, AND THE URL FOLLOWS.
//
// The search box names the rows it filters; each row carries the text a
// search looks in, lower-cased by the same function the server uses, so the
// two cannot disagree about what matches. The foot says how many are shown,
// and the URL keeps the search with replaceState -- a catalog URL carries no
// statement, so the SQL screen's rule against touching history is not this
// one's -- which is what makes a reload or a sent link show the same rows.
func TestTheCatalogFiltersWhileYouType(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/data")
	for _, want := range []string{`data-filter-for="catalog-rows"`, `id="catalog-rows"`,
		`data-filter-row`, `data-foot-total="3"`, `data-foot-noun="destinations"`, "data-filter-empty"} {
		if !strings.Contains(body, want) {
			t.Errorf("the catalog lacks %s", want)
		}
	}
	if !strings.Contains(body, `data-filter-text="s3://demo-landing/reports/`) || !strings.Contains(body, "shell_report") {
		t.Error("a row's search text is not where and who")
	}

	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{"[data-filter-for]", "data-filter-text", "replaceState", "data-foot-total", "data-filter-keep"} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js's list filter never uses %s", want)
		}
	}
}
