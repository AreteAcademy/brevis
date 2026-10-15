package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// readsFake answers the two reads the palette needs; anything else it is asked
// panics, which is also how a test learns the index reads more than it says.
type readsFake struct {
	api.Leitura
	workflows []postgres.WorkflowSummary
	projects  []postgres.ProjectSummary

	// recent answers RecentStates, and recentCalls counts the calls: the
	// strip is one query per page, not one per row.
	recent      map[string][]postgres.RecentRun
	recentCalls *int
}

func (f readsFake) RecentStates(_ context.Context, slugs []string, _ int) (map[string][]postgres.RecentRun, error) {
	if f.recentCalls != nil {
		*f.recentCalls++
	}
	out := map[string][]postgres.RecentRun{}
	for _, s := range slugs {
		if r, ok := f.recent[s]; ok {
			out[s] = r
		}
	}
	return out, nil
}

func (f readsFake) Workflows(context.Context) ([]postgres.WorkflowSummary, error) {
	return f.workflows, nil
}
func (f readsFake) Projects(context.Context) ([]postgres.ProjectSummary, error) {
	return f.projects, nil
}

type paletteItem struct {
	Group, Label, Hint, Href, Icon, Text string
}

func palette(t *testing.T, ui *api.UI) (int, http.Header, []paletteItem) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/palette", nil))
	var body struct{ Items []paletteItem }
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("the index is not JSON: %v\n%s", err, rec.Body.String())
		}
	}
	return rec.Code, rec.Header(), body.Items
}

func paletteUI() *api.UI {
	reads := readsFake{
		workflows: []postgres.WorkflowSummary{{Slug: "daily_sales", Name: "daily_sales", Project: "analytics"}},
		projects:  []postgres.ProjectSummary{{Slug: "analytics", Name: "analytics", Workflows: 12}},
	}
	return api.NewUI(reads, nil, nil, nil, nil, catalogFake{entries: demoCatalog()}, branding.Default(), slog.New(slog.DiscardHandler)).
		WithEndpoints(published())
}

func find(items []paletteItem, group, label string) *paletteItem {
	for i := range items {
		if items[i].Group == group && items[i].Label == label {
			return &items[i]
		}
	}
	return nil
}

// THE INDEX IS EVERYTHING THE CONSOLE LISTS, EACH WITH WHERE IT GOES.
//
// One JSON answer the palette fetches once and filters in the browser: the
// sections, then the workflows, projects, destinations and endpoints. Each
// item carries its search text, lower-cased by the server, the list filter's
// contract -- so what matches is decided in one place.
func TestThePaletteIndexesWhatTheConsoleLists(t *testing.T) {
	code, hdr, items := palette(t, paletteUI())
	if code != http.StatusOK {
		t.Fatalf("/api/palette answered %d", code)
	}
	for _, want := range []struct{ group, label, href string }{
		{"Sections", "Catalog", "/data"},
		{"Workflows", "daily_sales", "/workflows/daily_sales"},
		{"Projects", "analytics", "/workflows?project=analytics"},
		{"Destinations", "s3://demo-landing/reports/", "/data/target?u=s3%3A%2F%2Fdemo-landing%2Freports%2F"},
		{"Endpoints", "/v1/clicks", "/data/endpoints/metrics-api/clicks"},
	} {
		it := find(items, want.group, want.label)
		if it == nil {
			t.Errorf("the index has no %s %q", want.group, want.label)
			continue
		}
		if it.Href != want.href {
			t.Errorf("%s %q goes to %q, want %q", want.group, want.label, it.Href, want.href)
		}
		if it.Text != strings.ToLower(it.Text) || !strings.Contains(it.Text, strings.ToLower(want.label)) {
			t.Errorf("%s %q has search text %q", want.group, want.label, it.Text)
		}
	}
	// It names what a login can see: kept by nobody between requests.
	if cc := hdr.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("the index is cacheable: %q", cc)
	}
}

// AND ONLY WHAT EXISTS HERE. The sidebar offers SQL only with a SQL service
// and the index agrees, because both read one list; a console with no
// endpoints reader lists no endpoints and no error.
func TestThePaletteOffersOnlyWhatThisConsoleHas(t *testing.T) {
	_, _, items := palette(t, dataUI(catalogFake{entries: demoCatalog()}))
	if find(items, "Sections", "SQL") != nil {
		t.Error("the index offers SQL on a console with no SQL service")
	}
	for _, it := range items {
		if it.Group == "Endpoints" || it.Group == "Workflows" {
			t.Errorf("the index lists %s %q from a reader that is not there", it.Group, it.Label)
		}
	}
	if find(items, "Destinations", "bronze.payments") == nil {
		t.Error("an optional reader being absent took the others with it")
	}
}

// A GET, AND ONLY A GET.
func TestThePaletteIndexIsReadOnly(t *testing.T) {
	mux := http.NewServeMux()
	paletteUI().Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/palette", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a POST to the index answered %d", rec.Code)
	}
}

// THE ISLAND DRIVES THE PALETTE: Cmd/Ctrl-K from anywhere -- listened for in
// the capture phase, so an editor below cannot swallow it -- the index
// fetched from one place, a redirect or a refusal taken as "no index" and
// never parsed as data, the active row named to a screen reader, the dialog
// opened modally, Cmd-Enter to a new tab, and the opener revealed only here.
// The ranking and the keys are verified in a browser (P4); what Go can hold
// is that the pieces are there.
func TestTheIslandDrivesThePalette(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{
		`"/api/palette"`, ".redirected", "aria-activedescendant", "showModal", `"aria-expanded"`,
		`=== "k"`, `"keydown", function`, "}, true);", "window.open", "[data-palette-open]",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js's palette never uses %s", want)
		}
	}
	if n := strings.Count(js, `"/api/palette"`); n != 1 {
		t.Errorf("the index is fetched from %d places; one is how it is fetched once", n)
	}
}
