package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
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

var catalogRowRE = regexp.MustCompile(`<tr data-href="/data/target\?(?:[^"]*&amp;)?u=([^"&]+)"`)

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

// THE CATALOG IS IN THE MENU (screen-catalog T2), first of Data, with its
// letter -- g d -- and, when some destination needs a person, how many.
func TestTheCatalogIsInTheMenu(t *testing.T) {
	ui := dataUI(catalogFake{entries: catalogEntries()})
	nav := sections(render(t, ui, "/ingest/loads"))
	at := strings.Index(nav, `href="/data"`)
	if at < 0 {
		t.Fatal("the menu has no Catalog")
	}
	if ep := strings.Index(nav, `href="/data/endpoints"`); ep < at {
		t.Error("Catalog is not first of Data")
	}
	link := nav[at : at+strings.Index(nav[at:], "</a>")]
	for _, want := range []string{`data-go-key="d"`, ">Catalog<", "1 destination needs attention", ">1<"} {
		if !strings.Contains(link, want) {
			t.Errorf("the Catalog link has no %s:\n%s", want, link)
		}
	}
	if page := sections(render(t, ui, "/data")); !strings.Contains(page[strings.Index(page, `href="/data"`):], `aria-current="page"`) {
		t.Error("the catalog's own page does not mark it open")
	}

	fine := []postgres.CatalogEntry{catalogEntries()[1]}
	nav = sections(render(t, dataUI(catalogFake{entries: fine}), "/ingest/loads"))
	at = strings.Index(nav, `href="/data"`)
	if strings.Contains(nav[at:at+strings.Index(nav[at:], "</a>")], "needs attention") {
		t.Error("a catalog with nothing to look at has a badge")
	}
}

// THE PALETTE FINDS A DESTINATION, with who writes it, and opens it.
func TestThePaletteFindsADestination(t *testing.T) {
	_, _, items := palette(t, dataUI(catalogFake{entries: catalogEntries()}))
	for _, it := range items {
		if it.Group == "Catalog" && it.Label == "bigquery://acme/bronze/old" {
			if it.Hint != "vendors · fetch" || it.Href != "/data/target?u=bigquery%3A%2F%2Facme%2Fbronze%2Fold" || it.GroupLabel != "Catalog" {
				t.Errorf("the destination's entry = %+v", it)
			}
			return
		}
	}
	t.Error("the palette has no destination")
}

// drawerUI is the catalog with one destination's detail: the stale table,
// its loads, and an endpoint that reads it.
func drawerUI() *api.UI {
	entries := catalogEntries()
	now := time.Now()
	detail := &postgres.TargetDetail{CatalogEntry: entries[0], Loads: []postgres.TargetLoad{
		{RunID: "aaaaaaaa-0000-0000-0000-000000000001", Workflow: "vendors", Node: "fetch", LoadedAt: now.Add(-6 * time.Hour), Rows: i64(10)},
		{RunID: "aaaaaaaa-0000-0000-0000-000000000002", Workflow: "vendors", Node: "fetch", LoadedAt: now.Add(-5 * time.Hour), Rows: i64(12)},
	}}
	reads := endpointsFake{all: []postgres.PublishedEndpoint{{API: "metrics-api", Name: "old", Path: "/v1/old",
		Reads: []string{"bigquery://acme/bronze/old"}, PublishedAt: now}}}
	return dataUI(catalogFake{entries: entries, detail: detail}).WithEndpoints(reads)
}

// A DESTINATION OPENS IN A DRAWER OVER THE CATALOG (screen-catalog T3), drawn
// open by the server so the link works with no script: its status, each
// writer and why it stands there, its last loads -- each opening its run --
// what reads it, how it is configured, and the verbs.
func TestADestinationOpensInADrawer(t *testing.T) {
	code, body := get(t, drawerUI(), "/data/target?u="+url.QueryEscape("bigquery://acme/bronze/old")+"&status=attention")
	if code != http.StatusOK {
		t.Fatalf("the drawer answered %d", code)
	}
	if got := strings.Join(catalogRows(body), " "); got != "bigquery://acme/bronze/old" {
		t.Errorf("the catalog behind the drawer lost its filter: %q", got)
	}
	at := strings.Index(body, `<dialog id="target"`)
	if at < 0 {
		t.Fatal("there is no drawer")
	}
	drawer := body[at : at+strings.Index(body[at:], "</dialog>")]
	for _, want := range []string{
		" open", "bigquery://acme/bronze/old", "stale", "the best writer",
		"vendors · fetch", "missed its", `href="/runs/aaaaaaaa-0000-0000-0000-000000000002"`,
		"dashed = a slot missed", "/v1/old", "Credentials never reach this page",
		`data-copy="#target-uri"`, `href="/data?status=attention" data-target-close`,
	} {
		if !strings.Contains(drawer, want) {
			t.Errorf("the drawer has no %q", want)
		}
	}
}

// A DESTINATION NOBODY KNOWS opens the catalog saying so; a target that cannot
// be one is refused.
func TestAnUnknownDestinationSaysSo(t *testing.T) {
	code, body := get(t, drawerUI(), "/data/target?u="+url.QueryEscape("postgres://nowhere/x"))
	if code != http.StatusOK || !strings.Contains(body, "No destination postgres://nowhere/x") || strings.Contains(body, `<dialog id="target"`) {
		t.Errorf("an unknown destination answered %d", code)
	}
	if code, _ := get(t, drawerUI(), "/data/target?u="+strings.Repeat("x", 5000)); code != http.StatusBadRequest {
		t.Errorf("an oversized target answered %d", code)
	}
}

// THE DRAWER IS ALSO A FRAGMENT, for the row that opens it with a script.
func TestTheDrawerIsAFragment(t *testing.T) {
	mux := http.NewServeMux()
	drawerUI().Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, "/data/target?u="+url.QueryEscape("bigquery://acme/bronze/old"), nil)
	r.Header.Set("X-Brevis-Fragment", "target")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "<html") || !strings.HasPrefix(strings.TrimSpace(body), `<dialog id="target"`) {
		t.Errorf("the fragment answered %d: %.120s", rec.Code, body)
	}
}

// A ROW KEEPS THE FILTER IT WAS OPENED FROM, and a destination its manifest
// could not name opens the stream that writes it.
func TestACatalogRowKeepsItsFilter(t *testing.T) {
	entries := append(catalogEntries(), postgres.CatalogEntry{Kind: "bigquery", Writers: []postgres.CatalogWriter{
		{Gateway: &postgres.GatewayWriter{Name: "edge", Stream: "/v1/legacy", Role: "sink"}}}})
	_, body := get(t, dataUI(catalogFake{entries: entries}), "/data?kind=bigquery")
	if !strings.Contains(body, `data-href="/data/target?kind=bigquery&amp;u=bigquery%3A%2F%2Facme%2Fbronze%2Fold"`) {
		t.Error("a row does not keep the filter it was opened from")
	}
	if !strings.Contains(body, `data-href="/ingest/streams/edge/v1/legacy"`) {
		t.Error("an unnamed destination does not open its stream")
	}
}
