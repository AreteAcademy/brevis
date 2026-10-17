package api_test

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
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

// A LOAD'S PAGE IS ONE STEP ON ONE DESTINATION: its header says what and who,
// the bars and the table are THIS step's loads -- each bar opening its run --
// and another workflow writing the same file stays off it.
func TestALoadPageIsOneStepOnOneDestination(t *testing.T) {
	code, body := get(t, dataUI(loadCatalog()), "/ingest/loads/daily_sales/report")
	if code != http.StatusOK {
		t.Fatalf("the load page answered %d", code)
	}
	for _, want := range []string{
		"file:///data/reports/", "SDK load", "daily_sales", "report", "every day at 04:00",
		`href="/runs/8d36ef6e-0000-0000-0000-000000000001"`, `href="/runs/6420b0fd-0000-0000-0000-000000000002"`,
		"Rows per load", "Recent loads", ">149<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the load page has no %s", want)
		}
	}
	if strings.Contains(body, "77777777") || strings.Contains(body, "backfill") {
		t.Error("another workflow's loads on the same destination leaked onto this step's page")
	}
	if !strings.Contains(body, "Only the Go SDK sends its configuration") || !strings.Contains(body, `href="/workflows/daily_sales"`) {
		t.Error("a step that did not describe itself does not say so, with the workflow linked")
	}
}

// A STEP THAT WRITES SEVERAL DESTINATIONS ASKS WHICH, and ?target= opens one.
// A step that writes nothing is not found.
func TestALoadPageForAStepWithSeveralDestinations(t *testing.T) {
	ui := dataUI(loadCatalog())
	_, body := get(t, ui, "/ingest/loads/multi/split")
	for _, want := range []string{`href="/ingest/loads/multi/split?target=s3%3A%2F%2Flake%2Fa%2F"`, `?target=s3%3A%2F%2Flake%2Fb%2F"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the chooser has no %s", want)
		}
	}
	if code, _ := get(t, ui, "/ingest/loads/nobody/nothing"); code != http.StatusNotFound {
		t.Errorf("a step that writes nothing answered %d", code)
	}
	if code, _ := get(t, ui, "/ingest/loads/multi/split?target=s3://elsewhere/"); code != http.StatusNotFound {
		t.Errorf("a target the step does not write answered %d", code)
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
	if !strings.Contains(text, "0 of 370 read") {
		t.Error("the rows KPI does not say what was read")
	}
	if !strings.Contains(body, "text-state-failed") {
		t.Error("0 written of 370 read is not in the failed tone")
	}
	if strings.Contains(body, ">Rows per load</h2>") {
		t.Error("the bars still have a panel of their own")
	}
	if !strings.Contains(body, "each bar opens the run that wrote it") {
		t.Error("the bars lost their legend")
	}
	if strings.Contains(body, "uppercase") && strings.Contains(body, "SDK load ·") && strings.Index(body, "SDK load") < strings.Index(body, "<h1") {
		t.Error("the kind is still an eyebrow above the title")
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
