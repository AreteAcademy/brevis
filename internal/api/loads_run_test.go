package api_test

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// loadsRunUI is loadCatalog's console with the workflows' definitions --
// daily_sales takes no parameters, multi takes one -- and a scheduler that
// keeps what it was asked.
func loadsRunUI(spy *triggerSpy) *api.UI {
	one, every := 0, 0
	defs := mapDefs{one: &one, every: &every, defs: map[string]wf.Workflow{
		"daily_sales": {Slug: "daily_sales", Nodes: []wf.Node{{ID: "report", Run: "echo"}}},
		"multi":       {Slug: "multi", Params: []wf.Param{{Name: "full", Type: wf.ParamBool, Default: "false"}}, Nodes: []wf.Node{{ID: "split", Run: "echo"}}},
	}}
	cat := loadCatalog()
	cat.detail.CatalogEntry = cat.entries[0]
	return api.NewUI(nil, defs, nil, spy, nil, cat, branding.Default(), slog.New(slog.DiscardHandler))
}

func drawerFoot(t *testing.T, body string) string {
	t.Helper()
	d := drawerOf(t, body)
	return d[strings.Index(d, "data-dialog-foot"):]
}

// RUN NOW, FROM THE LOAD (screen-loads T6): a workflow with no parameters
// runs at once -- the trigger's form, posted by ui.js (data-run-now) so a
// refusal is said in the drawer, coming back to the same drawer, filter and
// all -- and one with parameters goes to its Run drawer.
func TestALoadRunsItsWorkflowNow(t *testing.T) {
	ui := loadsRunUI(&triggerSpy{})

	_, body := get(t, ui, "/ingest/loads/daily_sales/report?status=late")
	foot := drawerFoot(t, body)
	for _, want := range []string{`action="/workflows/daily_sales/trigger"`, "data-run-now",
		`name="back" value="/ingest/loads/daily_sales/report?status=late"`, "Run now", "data-run-error"} {
		if !strings.Contains(foot, want) {
			t.Errorf("the drawer's Run now has no %s", want)
		}
	}

	cat := loadCatalog()
	cat.detail.CatalogEntry = cat.entries[1] // s3://lake/a/, written by multi
	one, every := 0, 0
	withParams := api.NewUI(nil, mapDefs{one: &one, every: &every, defs: map[string]wf.Workflow{
		"multi": {Slug: "multi", Params: []wf.Param{{Name: "full", Type: wf.ParamBool, Default: "false"}}},
	}}, nil, &triggerSpy{}, nil, cat, branding.Default(), slog.New(slog.DiscardHandler))
	_, body = get(t, withParams, "/ingest/loads/multi/split?target="+url.QueryEscape("s3://lake/a/"))
	foot = drawerFoot(t, body)
	if !strings.Contains(foot, `href="/workflows/multi#run"`) || strings.Contains(foot, "/trigger") {
		t.Error("a workflow with parameters does not go to its Run drawer")
	}
}

// AND THE RUN COMES BACK TO THE DRAWER, saying it was made and where to
// follow it.
func TestARunFromALoadComesBackToIt(t *testing.T) {
	spy := &triggerSpy{}
	back := "/ingest/loads/daily_sales/report?status=late"
	rec := postTrigger(t, loadsRunUI(spy), "daily_sales", url.Values{"back": {back}})
	if !spy.called || rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != back {
		t.Fatalf("the run answered %d to %q (called %v)", rec.Code, rec.Header().Get("Location"), spy.called)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "brevis_flash") || !strings.Contains(cookie, "Run") || strings.Contains(cookie, "Rerun") {
		t.Errorf("coming back does not say a run -- not a rerun -- was made: %s", cookie)
	}
}

// NO SCHEDULER, NO RUN NOW: a console that cannot trigger offers nothing to
// press.
func TestALoadWithNoSchedulerOffersNoRun(t *testing.T) {
	_, body := get(t, dataUI(loadCatalog()), "/ingest/loads/daily_sales/report")
	if foot := drawerFoot(t, body); strings.Contains(foot, "/trigger") || strings.Contains(foot, "#run") {
		t.Error("a console with no scheduler offers a run")
	}
}
