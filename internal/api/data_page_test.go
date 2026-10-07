package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
}

func (c catalogFake) Catalog(context.Context) ([]postgres.CatalogEntry, error) {
	return c.entries, c.err
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
		if !strings.Contains(body, want) {
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
	if !strings.Contains(body, "s3://demo-landing/reports/") || strings.Contains(body, "landing_orders</span>") {
		t.Error("?kind=s3 did not narrow the table to the s3 destination")
	}
	if !strings.Contains(body, "showing 1 of 3") {
		t.Error("a filtered page does not say how much it shows")
	}

	_, body = get(t, ui, "/data?status=attention")
	if !strings.Contains(body, "Nothing matches this filter") {
		t.Error("an empty filter result does not say so")
	}

	_, body = get(t, ui, "/data?status=bogus")
	if !strings.Contains(body, "landing_orders") || strings.Contains(body, "showing ") {
		t.Error("an unknown status should be ignored, showing everything")
	}
}
