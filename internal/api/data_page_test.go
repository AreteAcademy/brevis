package api_test

import (
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

type catalogFake struct {
	entries []postgres.CatalogEntry
	err     error

	detail *postgres.TargetDetail
	asked  *string

	streams map[postgres.StreamKey]*postgres.WriterConfig
	steps   map[postgres.StepKey]*postgres.WriterConfig
}

func (c catalogFake) StepConfigs(context.Context) (map[postgres.StepKey]*postgres.WriterConfig, error) {
	return c.steps, nil
}

func (c catalogFake) StreamConfigs(context.Context) (map[postgres.StreamKey]*postgres.WriterConfig, error) {
	return c.streams, nil
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

// Catalog's tests, restated on the screens that inherited each behaviour
// (IN6): the list's on /ingest/loads, the destination page's on a load's page,
// the gateway's on /ingest/streams.

func TestTheLoadsPageListsEveryLoadAndItsWriter(t *testing.T) {
	code, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		"postgres://brevis_it/public/landing_orders", "sdk_orders", "load",
		"s3://demo-landing/reports/", "shell_report",
		"bronze.payments", "legacy",
		// What is listed, and what is not, is said under the title.
		"Writes made outside Brevis do not show here",
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

func TestTheLoadsPageSaysHowALoadAppears(t *testing.T) {
	code, body := get(t, dataUI(catalogFake{}), "/ingest/loads")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "No load yet") || !strings.Contains(body, "landed") {
		t.Errorf("the empty state does not say how a load appears:\n%s", body)
	}
}

// INGEST IS IN THE NAVIGATION, Streams and Loads. (The Catalog, which IN6
// had taken out, is back under Data: TestTheCatalogIsInTheMenu.)
func TestIngestIsInTheNavigation(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{}), "/ingest/loads")
	for _, want := range []string{`href="/ingest/streams"`, `href="/ingest/loads"`, `aria-label="Ingest"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the navigation has no %s", want)
		}
	}
}

// Every load carries its status as words, not colour alone, and the summary
// above the table counts them.
func TestTheLoadsPageShowsStatusesInWordsAndCountsThem(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads")
	for _, want := range []string{"on time", "no schedule", "3 destinations"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
}

func TestALateLoadLinksToTheRunThatIsAboutToFixIt(t *testing.T) {
	run := "7aa2549c-0503-5f93-ab58-0ab78e7eb555"
	late := []postgres.CatalogEntry{{
		Target: "postgres://db/public/orders", Kind: "postgres",
		Writers: []postgres.CatalogWriter{{Workflow: "orders_sync", Node: "load",
			LastLoaded: time.Now().Add(-90 * time.Minute), HasSchedule: true,
			Cron: "0 * * * *", Timezone: "UTC", Active: true, RunInFlight: &run}},
	}}
	_, body := get(t, dataUI(catalogFake{entries: late}), "/ingest/loads")
	if !strings.Contains(body, `href="/runs/`+run+`"`) || !strings.Contains(body, "run in progress") {
		t.Error("a late load with a run in flight does not link to it")
	}
}

func TestTheLoadsPageFiltersFromTheQueryString(t *testing.T) {
	ui := dataUI(catalogFake{entries: demoCatalog()})

	_, body := get(t, ui, "/ingest/loads?kind=s3")
	if !strings.Contains(body, "demo-landing") || strings.Contains(body, "landing_orders") {
		t.Error("?kind=s3 did not narrow the table to the s3 load")
	}
	_, body = get(t, ui, "/ingest/loads?status=attention")
	if !strings.Contains(body, "Nothing matches this filter") {
		t.Error("an empty filter result does not say so")
	}

	_, body = get(t, ui, "/ingest/loads?status=bogus")
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

// loadUI is a console holding one destination, listed and detailed, so its
// load's page resolves.
func loadUI(d *postgres.TargetDetail, asked *string) *api.UI {
	return dataUI(catalogFake{detail: d, asked: asked, entries: []postgres.CatalogEntry{d.CatalogEntry}})
}

func TestALoadPageShowsItsWriterAndLoads(t *testing.T) {
	d := lateDetail()
	var asked string
	code, body := get(t, loadUI(d, &asked), "/ingest/loads/orders_sync/load")
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

func TestTheListLinksEachLoadToItsPage(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads")
	if !strings.Contains(body, `href="/ingest/loads/shell_report/build_report"`) {
		t.Error("the list does not link to the load page")
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

// A GATEWAY'S DESTINATIONS ARE ITS STREAMS', and one its manifest could not
// name is said to be so, with why -- inside its stream, not as a row.
func TestTheStreamsPageShowsGatewayDestinations(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: gatewayCatalog()}), "/ingest/streams")
	for _, want := range []string{
		"pubsub://brevis-local/clicks", "continuous", "edge", "/v1/clicks",
		"unidentified", "relative to the gateway", "/metrics",
	} {
		if !strings.Contains(visible(body), want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	if strings.Count(body, "data-stream-card") != 1 {
		t.Error("the unnamed dead letter became a stream of its own")
	}
}

// visible is the page's text as a reader sees it: tags removed and entities
// decoded. A URI drawn as a dimmed prefix and its object is two elements in
// the markup and one string on the screen, and the screen is what these
// tests are about.
func visible(body string) string {
	return html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, ""))
}

func TestAStreamPageNamesTheGatewayThatPublishedIt(t *testing.T) {
	e := gatewayCatalog()[0]
	_, body := get(t, dataUI(catalogFake{entries: gatewayCatalog(), detail: &postgres.TargetDetail{CatalogEntry: e}}),
		"/ingest/streams/edge/v1/clicks")
	for _, want := range []string{"gateway stream", "edge", "/metrics", "published"} {
		if !strings.Contains(visible(body), want) {
			t.Errorf("the stream page does not say %q", want)
		}
	}
}

// LOADS ARE SEARCHED BY WHERE AND BY WHO: a destination's URI, and the
// workflow or step that writes it -- case-insensitive, because nobody
// remembers whether it was `Orders` or `orders`. On the SERVER, so a search is
// a link and works with no script; ui.js only makes it answer while somebody
// types.
func TestTheLoadsAreSearchedByWhereAndByWho(t *testing.T) {
	ui := dataUI(catalogFake{entries: demoCatalog()})

	_, body := get(t, ui, "/ingest/loads?q=REPORTS")
	if rowHidden(t, body, "demo-landing") || !rowHidden(t, body, "landing_orders") || !rowHidden(t, body, "payments") {
		t.Error("?q=REPORTS did not narrow to the load whose URI says reports")
	}
	if !strings.Contains(body, `value="REPORTS"`) {
		t.Error("the search box does not keep what was searched")
	}

	_, body = get(t, ui, "/ingest/loads?q=sdk_orders")
	if rowHidden(t, body, "landing_orders") || !rowHidden(t, body, "demo-landing") {
		t.Error("?q=sdk_orders did not find the load that workflow writes")
	}

	_, body = get(t, ui, "/ingest/loads?q=nothing-is-called-this")
	empty := body[strings.LastIndex(body[:strings.Index(body, "Nothing matches this filter")], "<div"):]
	if strings.Contains(empty[:strings.Index(empty, ">")], "hidden") || !strings.Contains(body, `href="/ingest/loads"`) {
		t.Error("a search that finds nothing does not say so and offer the way back")
	}
}

// A SEARCH DOES NOT DROP THE OTHER FILTERS, AND A FILTER DOES NOT DROP THE
// SEARCH: each is one field of the same URL.
func TestTheSearchAndTheFiltersKeepEachOther(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads?kind=s3&q=report")
	if !strings.Contains(body, `<input type="hidden" name="kind" value="s3"`) {
		t.Error("searching again would drop the kind filter")
	}
	if !strings.Contains(body, `href="/ingest/loads?status=on_time&amp;kind=s3&amp;q=report"`) &&
		!strings.Contains(body, `href="/ingest/loads?kind=s3&amp;q=report"`) {
		t.Error("the filter links drop the search")
	}
}

// A URI IS DRAWN AS WHERE, THEN WHAT. The scheme and host are the same on
// most rows and the eye needs the part that differs, so the prefix is dimmed
// and the object is not -- the whole URI is still there to read and select.
// Cut where the Catalog cuts it (screen-loads T2): at the table.
func TestAURIIsDrawnAsWhereThenWhat(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads")
	for _, want := range []string{">postgres://brevis_it/public/<", ">landing_orders<", ">bronze.<", ">payments<"} {
		if !strings.Contains(body, want) {
			t.Errorf("the URI is not split where it should be: no %s", want)
		}
	}
}

// THE WHOLE ROW GOES TO THE LOAD, without taking the URI's own link away: the
// row carries where it goes, ui.js follows it on a click that did not land on
// a link, and the URI stays a real link for the keyboard and for no script.
func TestTheWholeRowGoesToTheLoad(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads")
	if !strings.Contains(body, `data-href="/ingest/loads/shell_report/build_report"`) {
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
// and the URL keeps the search with replaceState -- a list URL carries no
// statement, so the SQL screen's rule against touching history is not this
// one's -- which is what makes a reload or a sent link show the same rows.
func TestTheLoadsFilterWhileYouType(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads")
	for _, want := range []string{`data-filter-for="load-rows"`, `id="load-rows"`,
		`data-filter-row`, "data-filter-empty"} {
		if !strings.Contains(body, want) {
			t.Errorf("the loads list lacks %s", want)
		}
	}
	if !strings.Contains(body, `data-filter-text="shell_report`) || !strings.Contains(body, "s3://demo-landing/reports/") {
		t.Error("a row's search text is not who and where")
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

// A LOAD IS TITLED BY ITS URI, IN THE MACHINE'S FACE: the URI is what
// somebody came to read and what they will paste. UNDER it, one meta line:
// how it stands and what kind of thing it is (screen-load-v2 LV2 moved it
// from an eyebrow above the title, folding in the "written by" line).
func TestALoadIsTitledByItsURI(t *testing.T) {
	d := lateDetail()
	_, body := get(t, loadUI(d, nil), "/ingest/loads/orders_sync/load")

	h1 := body[strings.Index(body, "<h1"):]
	h1 = h1[:strings.Index(h1, "</h1>")]
	if !strings.Contains(h1, d.Target) || !strings.Contains(h1, "font-mono") {
		t.Errorf("the title is not the URI in mono: %s", h1)
	}
	if !strings.Contains(h1, `id="destination-uri"`) {
		t.Error("the title has no id for Copy URI to read")
	}
	meta := body[strings.Index(body, "data-load-meta"):]
	meta = meta[:strings.Index(meta, "</p>")]
	// LATE OR STALE, BY THE CLOCK. The fixture loaded 90 minutes ago on an
	// hourly schedule, so which one it is depends on the minute the test
	// runs -- pinning one made this test fail at the top of some hours.
	if !strings.Contains(meta, "late") && !strings.Contains(meta, "stale") {
		t.Errorf("the meta line does not say how the load stands: %s", meta)
	}
	if !strings.Contains(meta, "postgres") {
		t.Errorf("the meta line does not say what it is: %s", meta)
	}
}

// THE PAGE'S VERBS ARE IN ITS HEADER: copy the URI, and -- only where there
// is a SQL service to ask -- open it in the workbench. A button that leads to
// "not configured" is a question nobody can act on.
func TestALoadsVerbsAreInItsHeader(t *testing.T) {
	d := lateDetail()
	_, body := get(t, loadUI(d, nil), "/ingest/loads/orders_sync/load")
	header := body[strings.Index(body, "data-page-header"):]
	header = header[:strings.Index(header, "</header>")]
	if !strings.Contains(header, `data-copy="#destination-uri"`) {
		t.Error("the header offers no Copy URI")
	}
	if strings.Contains(body, "/sql?target=") {
		t.Error("a console with no SQL service offers to query in SQL")
	}

	svc := (&sqlFake{body: `{"columns":["k"],"rows":[["1"]]}`}).start(t)
	body = render(t, withPreview(t, sqlserve.New(svc.URL, "")), "/ingest/loads/bronze/load")
	header = body[strings.Index(body, "data-page-header"):]
	header = header[:strings.Index(header, "</header>")]
	if !strings.Contains(header, "/sql?target=") {
		t.Error("Query in SQL is not one of the header's verbs")
	}
}

// THE CONFIGURATION IS THE PAGE; ITS LOADS AND WHO WRITES IT SIT BESIDE IT
// (screen-load-v2 LV5 moved the loads to the side and the side's
// Destination panel to the head, the strip and the Destination tab). The
// side column is after the main one in the document, so a narrow screen
// stacks it below rather than above what somebody came for.
func TestALoadKeepsItsWriterAndDestinationBeside(t *testing.T) {
	d := lateDetail()
	_, body := get(t, loadUI(d, nil), "/ingest/loads/orders_sync/load")
	side := strings.Index(body, "data-side")
	if side < 0 {
		t.Fatal("the page has no side column")
	}
	if strip := strings.Index(body, "Rows per load"); strip < 0 || strip > side {
		t.Error("the bars are not in the strip above the columns")
	}
	for _, want := range []string{">Recent loads<", ">Written by<", "orders_sync", "0 * * * *"} {
		if i := strings.Index(body[side:], want); i < 0 {
			t.Errorf("the side column does not carry %s", want)
		}
	}
	if !strings.Contains(body[:side], "postgres") {
		t.Error("what the destination is left the page")
	}
}

// /ingest/loads ANSWERS, one row per step and destination (screen-loads T2);
// the collapse and group filter in ui.js are the Streams list's now.
func TestTheLoadsPageAnswers(t *testing.T) {
	code, body := get(t, dataUI(catalogFake{entries: demoCatalog()}), "/ingest/loads?legacy=1")
	if code != http.StatusOK {
		t.Fatalf("/ingest/loads answered %d", code)
	}
	if !strings.Contains(body, `href="/workflows/payments"`) || strings.Contains(body, `href="/workflows/sdk_orders"`) {
		t.Error("legacy-only did not narrow the rows")
	}
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	for _, want := range []string{"data-filter-group"} {
		if !strings.Contains(strip(raw), want) {
			t.Errorf("ui.js has no %s", want)
		}
	}
}

// /ingest/streams ANSWERS, and fills each stream's configuration from the one
// read of every stream's.
func TestTheStreamsPageAnswers(t *testing.T) {
	entries := []postgres.CatalogEntry{
		{Target: "pubsub://local/clicks", Kind: "pubsub", Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{Name: "edge", Stream: "/v1/clicks", Role: "sink"}}}},
		{Target: "file:///dl/clicks/", Kind: "file", Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{Name: "edge", Stream: "/v1/clicks", Role: "dead_letter"}}}},
	}
	cfg := &postgres.WriterConfig{Fields: []catalog.ManifestField{{Path: "format", Value: "ndjson"}, {Path: "sink.type", Value: "pubsub"}}}
	ui := dataUI(catalogFake{entries: entries, streams: map[postgres.StreamKey]*postgres.WriterConfig{{Gateway: "edge", Path: "/v1/clicks"}: cfg}})
	code, body := get(t, ui, "/ingest/streams")
	if code != http.StatusOK {
		t.Fatalf("/ingest/streams answered %d", code)
	}
	if strings.Count(body, "data-stream-card") != 1 || !strings.Contains(body, "ndjson") {
		t.Error("the stream is not drawn once, with its configuration")
	}
}

// A STREAM'S PAGE ANSWERS at /ingest/streams/<gateway>/<path>, reading the
// sink's detail for the configuration's history; a stream nobody published is
// not found.
func TestTheStreamPageAnswers(t *testing.T) {
	sink := postgres.CatalogEntry{Target: "pubsub://local/clicks", Kind: "pubsub", Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{Name: "edge", Stream: "/v1/clicks", Role: "sink"}}}}
	var asked string
	ui := dataUI(catalogFake{entries: []postgres.CatalogEntry{sink}, detail: &postgres.TargetDetail{CatalogEntry: sink}, asked: &asked})
	code, body := get(t, ui, "/ingest/streams/edge/v1/clicks")
	if code != http.StatusOK || !strings.Contains(body, "POST /v1/clicks") {
		t.Fatalf("the stream page answered %d", code)
	}
	if asked != "pubsub://local/clicks" {
		t.Errorf("the page read %q, want the sink's detail", asked)
	}
	if code, _ := get(t, ui, "/ingest/streams/edge/v1/nothing"); code != http.StatusNotFound {
		t.Errorf("an unknown stream answered %d", code)
	}
	_, js := get(t, ui, "/assets/ui.js")
	for _, want := range []string{"[data-hash-tabs]", "hashchange", "[data-tab-panel]", "[data-test-body]"} {
		if !strings.Contains(strip(js), want) {
			t.Errorf("ui.js has no %s", want)
		}
	}
}
