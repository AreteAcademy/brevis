package api_test

import (
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
	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// loadCatalog is daily_sales' report landing on a file, a second workflow
// writing the same file (whose loads must not leak into report's page), and
// a step that writes two destinations.
func loadCatalog() catalogFake {
	now := time.Now()
	report := postgres.CatalogWriter{Workflow: "daily_sales", Node: "report", LastLoaded: now.Add(-47 * time.Minute),
		LastRows: i64(4), HasSchedule: true, Cron: "0 4 * * *", Timezone: "UTC", Active: true}
	other := postgres.CatalogWriter{Workflow: "backfill", Node: "dump", LastLoaded: now.Add(-time.Hour), LastRows: i64(9)}
	entry := postgres.CatalogEntry{Target: "file:///data/reports/", Kind: "file", Writers: []postgres.CatalogWriter{report, other}}
	return catalogFake{
		entries: []postgres.CatalogEntry{
			entry,
			{Target: "s3://lake/a/", Kind: "s3", Writers: []postgres.CatalogWriter{{Workflow: "multi", Node: "split", LastLoaded: now}}},
			{Target: "s3://lake/b/", Kind: "s3", Writers: []postgres.CatalogWriter{{Workflow: "multi", Node: "split", LastLoaded: now}}},
		},
		detail: &postgres.TargetDetail{CatalogEntry: entry, Loads: []postgres.TargetLoad{
			{RunID: "8d36ef6e-0000-0000-0000-000000000001", Workflow: "daily_sales", Node: "report", LoadedAt: now.Add(-47 * time.Minute), Rows: i64(4), Bytes: i64(149)},
			{RunID: "77777777-0000-0000-0000-000000000009", Workflow: "backfill", Node: "dump", LoadedAt: now.Add(-time.Hour), Rows: i64(9)},
			{RunID: "6420b0fd-0000-0000-0000-000000000002", Workflow: "daily_sales", Node: "report", LoadedAt: now.Add(-25 * time.Hour), Rows: i64(0)},
		}},
	}
}

// drawerOf is the load's drawer out of a page: from its <dialog> to its end.
func drawerOf(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, `<dialog id="load"`)
	if at < 0 {
		t.Fatalf("there is no load drawer:\n%.400s", body)
	}
	return body[at : at+strings.Index(body[at:], "</dialog>")]
}

// A LOAD IS ONE STEP ON ONE DESTINATION, IN A DRAWER OVER THE LIST
// (screen-loads T4), at the address its page had: the list behind it, drawn
// open by the server; its head says what and who, the bars and the table are
// THIS step's loads -- each opening its run -- and another workflow writing
// the same file stays off it.
func TestALoadPageIsOneStepOnOneDestination(t *testing.T) {
	code, body := get(t, dataUI(loadCatalog()), "/ingest/loads/daily_sales/report")
	if code != http.StatusOK {
		t.Fatalf("the load answered %d", code)
	}
	if !strings.Contains(body, `id="loads-list"`) {
		t.Error("the list is not behind the drawer")
	}
	drawer := drawerOf(t, body)
	for _, want := range []string{
		" open", "Batch load · daily_sales", ">report<", "every day at 04:00", "file:///data/reports/",
		`href="/runs/8d36ef6e-0000-0000-0000-000000000001"`, `href="/runs/6420b0fd-0000-0000-0000-000000000002"`,
		"Rows per load", "Recent loads", ">149<", "A sudden drop usually means",
		`href="/workflows/daily_sales"`, `href="/ingest/loads"`,
	} {
		if !strings.Contains(drawer, want) {
			t.Errorf("the load's drawer has no %s", want)
		}
	}
	if strings.Contains(drawer, "77777777") || strings.Contains(drawer, "backfill") {
		t.Error("another workflow's loads on the same destination leaked into this step's drawer")
	}
	if !strings.Contains(drawer, "Only the Go SDK sends its configuration") {
		t.Error("a step that did not describe itself does not say so")
	}
}

// THE DRAWER IS A FRAGMENT TOO: asked for alone (X-Brevis-Fragment: load), it
// is the <dialog> with nothing around it, for the row's script (T7).
func TestTheLoadDrawerIsAFragment(t *testing.T) {
	mux := http.NewServeMux()
	dataUI(loadCatalog()).Registrar(mux)
	r := httptest.NewRequest(http.MethodGet, "/ingest/loads/daily_sales/report?status=late", nil)
	r.Header.Set("X-Brevis-Fragment", "load")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(body), `<dialog id="load"`) {
		t.Fatalf("the fragment answered %d: %.200s", rec.Code, body)
	}
	// Closing goes back to the list as it was filtered.
	if !strings.Contains(body, `href="/ingest/loads?status=late" data-target-close`) {
		t.Error("the × does not keep the list's filter")
	}
}

// A STEP THAT WRITES SEVERAL DESTINATIONS ASKS WHICH, in its drawer, and
// ?target= opens one with the others a click away. A step that writes
// nothing, or a destination it does not write, is the list with a note.
func TestALoadPageForAStepWithSeveralDestinations(t *testing.T) {
	cat := loadCatalog()
	cat.detail = &postgres.TargetDetail{CatalogEntry: cat.entries[2]} // s3://lake/b/
	ui := dataUI(cat)
	_, body := get(t, ui, "/ingest/loads/multi/split")
	drawer := drawerOf(t, body)
	for _, want := range []string{`href="/ingest/loads/multi/split?target=s3%3A%2F%2Flake%2Fa%2F"`, `?target=s3%3A%2F%2Flake%2Fb%2F"`, "writes 2 destinations"} {
		if !strings.Contains(drawer, want) {
			t.Errorf("the chooser has no %s", want)
		}
	}
	_, body = get(t, ui, "/ingest/loads/multi/split?target="+url.QueryEscape("s3://lake/b/"))
	if d := drawerOf(t, body); !strings.Contains(d, `aria-current="true"`) || !strings.Contains(d, "?target=s3%3A%2F%2Flake%2Fa%2F") {
		t.Error("an open destination does not keep the others a click away")
	}
	for _, path := range []string{"/ingest/loads/nobody/nothing", "/ingest/loads/multi/split?target=s3://elsewhere/"} {
		code, body := get(t, ui, path)
		if code != http.StatusOK || !strings.Contains(body, "writes no destination Brevis knows") || strings.Contains(body, `<dialog id="load"`) {
			t.Errorf("%s answered %d without the note, or with a drawer", path, code)
		}
	}
}

// A LOAD SAYS WHAT IT READ beside what it wrote (screen-load-v2 LV2): the
// newest load's run has the step's stages, and a load that wrote 0 of 370
// is drawn in the failed tone. The head carries who writes it and when; the
// bars sit in the strip, their legend in the cell's title.
func TestALoadSaysHowManyRowsItRead(t *testing.T) {
	cat := loadCatalog()
	cat.detail.Loads[0].Rows = i64(0)
	cat.detail.Writers[0].LastRows = i64(0)
	execs := execsFake{states: map[string]postgres.NodeState{"report": {Stages: []postgres.Stage{
		{Name: "extract", Numbers: map[string]any{"pages": 10.0}},
		{Name: "map", Numbers: map[string]any{"in": 370.0, "out": 370.0}},
		{Name: "load", Numbers: map[string]any{"rows": 0.0}},
	}}}}
	ui := api.NewUI(nil, nil, execs, nil, nil, cat, branding.Default(), slog.New(slog.DiscardHandler))
	_, body := get(t, ui, "/ingest/loads/daily_sales/report")

	text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " ")), " ")
	if !strings.Contains(text, "0 rows of 370 read") {
		t.Error("the last load does not say what was read")
	}
	if !strings.Contains(body, "text-state-failed") {
		t.Error("0 written of 370 read is not in the failed tone")
	}
	if !strings.Contains(body, "each bar opens its run") {
		t.Error("the bars lost their legend")
	}

	mapped := execs
	mapped.states = map[string]postgres.NodeState{"report": {Instances: 3, Done: 3, Stages: execs.states["report"].Stages}}
	_, m := get(t, api.NewUI(nil, nil, mapped, nil, nil, loadCatalog(), branding.Default(), slog.New(slog.DiscardHandler)), "/ingest/loads/daily_sales/report")
	if strings.Contains(m, "of 370 read") {
		t.Error("a mapped step's load claims one instance's read count")
	}

	_, plain := get(t, dataUI(loadCatalog()), "/ingest/loads/daily_sales/report")
	if strings.Contains(plain, " read<") || strings.Contains(plain, "of 370") {
		t.Error("a load with no stages to read claims a read count")
	}
}

// A LOAD'S CONFIGURATION IS IN TABS (screen-load-v2 LV3): Summary, Sources,
// Transform, Destination, Schema -- hash tabs, every panel in the HTML with
// its heading -- and what three sources share is said once.
func TestALoadsConfigurationIsInTabs(t *testing.T) {
	cat := loadCatalog()
	cat.detail.Writers[0].Config = &postgres.WriterConfig{Fields: []catalog.ManifestField{
		{Path: "source.driver", Value: "from.Many"}, {Path: "source.workers", Value: 2.0},
		{Path: "source.sources.1.driver", Value: "from.Files"}, {Path: "source.sources.1.path", Value: "/in/a.csv"},
		{Path: "source.sources.2.driver", Value: "from.Files"}, {Path: "source.sources.2.path", Value: "/in/b.csv"},
		{Path: "source.sources.3.driver", Value: "from.Files"}, {Path: "source.sources.3.path", Value: "/in/c.csv"},
		{Path: "transform.1.map", Value: []any{"compute(loaded_by)"}},
		{Path: "target.driver", Value: "to.Files"}, {Path: "target.target", Value: "file:///data/reports/"},
		{Path: "target.declared.columns", Value: []any{"sku", "qty"}},
	}}
	_, body := get(t, dataUI(cat), "/ingest/loads/daily_sales/report")

	for _, want := range []string{`data-hash-tabs`, `data-tab-panel="summary"`, `data-tab-panel="sources"`,
		`data-tab-panel="transform"`, `data-tab-panel="destination"`, `data-tab-panel="schema"`,
		`data-tab="sources"`, ">Sources 3<", ">Transform 1<", ">Schema 2<", "data-panel-heading", "Common to 3"} {
		if !strings.Contains(body, want) {
			t.Errorf("the load's configuration has no %s", want)
		}
	}
	if strings.Count(body, "from.Files") != 1 {
		t.Errorf("the shared driver is said %d times, not once", strings.Count(body, "from.Files"))
	}
	for _, p := range []string{"/in/a.csv", "/in/b.csv", "/in/c.csv", "compute(loaded_by)", "file:///data/reports/"} {
		if !strings.Contains(body, p) {
			t.Errorf("the configuration lost %s", p)
		}
	}
	if !strings.Contains(body, "hashTabs") && !strings.Contains(body, `[data-tab-panel="sources"]{display:block}`) {
		t.Error("the page does not write the rule that opens #sources first")
	}
}

// NOTHING THE LOAD PAGE SHOWED IS LOST (screen-load-v2 LV5), now in its
// drawer (screen-loads T4): every value of
// the spec's list is still on it after the regrouping -- each load's when,
// rows, bytes and run; the bars and their legend; who writes it, on what
// schedule, how it stands; what it is and where. The loads now sit beside
// the page, and the side's "Destination" panel went to the head, the strip
// and the Destination tab.
func TestNothingTheLoadPageShowedIsLost(t *testing.T) {
	_, body := get(t, dataUI(loadCatalog()), "/ingest/loads/daily_sales/report")
	text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " ")), " ")
	for _, want := range []string{
		"8d36ef6e", "6420b0fd", "149", // runs, and the one load with bytes
		"each bar opens its run",                                   // the bars' legend
		"daily_sales", "report", "every day at 04:00", "0 4 * * *", // writer and schedule
		"file:///data/reports/", "file", // what and where
	} {
		if !strings.Contains(text, want) && !strings.Contains(body, want) {
			t.Errorf("the load page lost %q", want)
		}
	}
	drawer := drawerOf(t, body)
	if !strings.Contains(drawer, ">Recent loads<") || !strings.Contains(drawer, `class="ct `) {
		t.Error("the recent loads are not in the drawer, as a compact table")
	}
}

// THE LIST SAYS WHICH DRAWER IS ITS OWN (screen-loads T7), for the one
// script that opens it in place on both screens: the fragment to ask for and
// the addresses that are a drawer; the drawer is marked as one, and a
// destination choice inside it swaps it in place.
func TestTheLoadsListNamesItsDrawerForTheScript(t *testing.T) {
	_, body := get(t, dataUI(loadCatalog()), "/ingest/loads/multi/split")
	for _, want := range []string{`id="loads-list" data-drawer-list data-drawer-fragment="load" data-drawer-path="/ingest/loads/"`,
		`<dialog id="load" class="drawer" aria-labelledby="load-title" open data-url-drawer`, "data-drawer-swap"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page has no %s", want)
		}
	}
	_, ui := browsing(t)
	_, js := get(t, ui, "/assets/ui.js")
	for _, want := range []string{"[data-drawer-list]", "data-drawer-fragment", "dialog[data-url-drawer]", "data-drawer-swap", "pushState"} {
		if !strings.Contains(strip(js), want) {
			t.Errorf("ui.js does not read %s", want)
		}
	}
}
