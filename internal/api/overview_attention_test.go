package api_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// failingList answers the failing workflows, as RunRepo would.
type failingList struct{ list []postgres.FailingWorkflow }

func (f failingList) FailingWorkflows(context.Context) ([]postgres.FailingWorkflow, error) {
	return f.list, nil
}
func (f failingList) FailureStreak(context.Context, string) (int, error) { return 0, nil }

// attentionExecs answers each failed run and its logs, and counts the logs
// read.
type attentionExecs struct {
	execsFake
	runs  map[uuid.UUID]dom.Run
	logs  map[uuid.UUID][]postgres.StepLog
	reads *int
}

func (e attentionExecs) Get(_ context.Context, id uuid.UUID) (dom.Run, error) {
	if r, ok := e.runs[id]; ok {
		return r, nil
	}
	return dom.Run{}, pgx.ErrNoRows
}
func (e attentionExecs) LogsDaRun(_ context.Context, id uuid.UUID) ([]postgres.StepLog, error) {
	*e.reads++
	return e.logs[id], nil
}

type failed struct {
	slug   string
	ago    time.Duration
	streak int
	log    string
	params map[string]string
}

func attentionUI(t *testing.T, fs ...failed) (*api.UI, *int) {
	t.Helper()
	return attentionUIWith(t, nil, fs...)
}

// attentionUIWith also has runs in flight; the workflow every failure belongs
// to declares one param, `region`.
func attentionUIWith(t *testing.T, inFlight []postgres.RunSummary, fs ...failed) (*api.UI, *int) {
	t.Helper()
	reads := 0
	e := attentionExecs{runs: map[uuid.UUID]dom.Run{}, logs: map[uuid.UUID][]postgres.StepLog{}, reads: &reads}
	var list []postgres.FailingWorkflow
	for _, f := range fs {
		id := uuid.New()
		list = append(list, postgres.FailingWorkflow{Slug: f.slug, RunID: id, FailedAt: time.Now().Add(-f.ago), Streak: f.streak})
		e.runs[id] = dom.Run{ID: id, WorkflowSlug: f.slug, Status: dom.StatusFailed, Params: f.params}
		code := 1
		e.logs[id] = []postgres.StepLog{{NodeID: "test", MapIndex: dom.Unmapped, Status: "failed", ExitCode: &code, Log: f.log}}
	}
	def := defsFake{w: wf.Workflow{Params: []wf.Param{{Name: "region", Type: wf.ParamString}}}}
	ui := api.NewUI(overviewReads{inFlight: inFlight, asked: &struct {
		span  time.Duration
		grain postgres.Grain
		n     int
	}{}}, def, e, &triggerSpy{}, nil, nil, branding.Default(), slog.New(slog.DiscardHandler)).WithFailing(failingList{list})
	return ui, &reads
}

func attentionOf(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, "data-attention")
	if at < 0 {
		t.Fatal("no attention section")
	}
	end := strings.Index(body[at:], "</section>")
	return body[at : at+end]
}

// WHAT NEEDS YOU COMES FIRST: each workflow whose last run failed, the ones
// failing again and again before the rest, each with why -- the step, how it
// exited, the line -- read from the run's log as its own page reads it.
func TestTheOverviewOpensWithWhatNeedsYou(t *testing.T) {
	ui, _ := attentionUI(t,
		failed{"environment_alert", 5 * time.Minute, 1, "1 of 2 PASS a\nError: not_null_alerts_id failed\n", nil},
		failed{"vendors_alert_ipma", time.Hour, 3, "Timeout of 60s fetching https://api.ipma.pt\n", nil},
	)
	_, body := get(t, ui, "/")
	if strings.Index(body, "data-attention") > strings.Index(body, "data-kpis") {
		t.Error("what needs you is not the first section")
	}
	att := attentionOf(t, body)
	if strings.Index(att, "vendors_alert_ipma") > strings.Index(att, "environment_alert") {
		t.Error("a workflow failing again and again is not first")
	}
	for _, want := range []string{
		"Failed 3 times in a row", "Step test failed (exit 1)", "Error: not_null_alerts_id failed",
		"failed 5 min ago", `#logs"`,
	} {
		if !strings.Contains(att, want) {
			t.Errorf("the attention lacks %q", want)
		}
	}
	if n := regexp.MustCompile(`data-attention-item=`).FindAllStringIndex(att, -1); len(n) != 2 {
		t.Errorf("%d items; wanted 2", len(n))
	}
}

func TestASecondFailureSaysSo(t *testing.T) {
	ui, _ := attentionUI(t, failed{"daily", time.Minute, 2, "Error: x\n", nil})
	_, body := get(t, ui, "/")
	if !strings.Contains(attentionOf(t, body), "2nd time in a row") {
		t.Error("a second failure in a row does not say so")
	}
}

// EIGHT, AND HOW MANY MORE. Each item's cause is a log read, so the section
// reads eight and counts the rest, with the way to all of them.
func TestTheAttentionShowsEightAndCountsTheRest(t *testing.T) {
	var fs []failed
	for i := 0; i < 11; i++ {
		fs = append(fs, failed{fmt.Sprintf("wf_%02d", i), time.Duration(i+1) * time.Minute, 1, "Error: x\n", nil})
	}
	ui, reads := attentionUI(t, fs...)
	_, body := get(t, ui, "/")
	att := attentionOf(t, body)
	if n := strings.Count(att, "data-attention-item="); n != 8 {
		t.Errorf("%d items; wanted 8", n)
	}
	if *reads != 8 {
		t.Errorf("%d logs read; wanted 8", *reads)
	}
	if !strings.Contains(att, `href="/runs?state=failed"`) || !strings.Contains(att, "and 3 more failing") {
		t.Error("the rest is not counted, with the way to it")
	}
	if !strings.Contains(att, ">11<") {
		t.Error("the section does not count every failing workflow")
	}
}

func TestNothingNeedsYou(t *testing.T) {
	ui, _ := attentionUI(t)
	_, body := get(t, ui, "/")
	att := attentionOf(t, body)
	if !strings.Contains(att, "Nothing needs you right now") || strings.Contains(att, "data-attention-item") {
		t.Error("an empty attention does not say all is well")
	}
}

func postTrigger(t *testing.T, ui *api.UI, slug string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodPost, "/workflows/"+slug+"/trigger", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// RERUN FROM THE OVERVIEW COMES BACK TO IT, saying the run was made and
// where to follow it -- the page the reader is watching is the list.
func TestARerunFromTheOverviewComesBack(t *testing.T) {
	spy := &triggerSpy{}
	ui := api.NewUI(nil, nil, nil, spy, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
	rec := postTrigger(t, ui, "daily", url.Values{"back": {"/"}, "param.region": {"br"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("answered %d to %q; wanted 303 to /", rec.Code, rec.Header().Get("Location"))
	}
	cookie, _ := url.QueryUnescape(rec.Header().Get("Set-Cookie"))
	if !strings.Contains(cookie, "Rerun of daily created") || !regexp.MustCompile(`/runs/[0-9a-f-]{36}`).MatchString(cookie) {
		t.Errorf("the flash does not say it, with the run: %s", cookie)
	}
	if spy.got["region"] != "br" {
		t.Errorf("the rerun lost its values: %v", spy.got)
	}
}

// Only a path on this site: back is the reader's to send, and a redirect
// anywhere is a phishing link with this console's name on it.
func TestABackOffThisSiteIsIgnored(t *testing.T) {
	for _, back := range []string{"//evil.example", "https://evil.example/", `/\evil.example`, "javascript:alert(1)", "runs"} {
		ui := api.NewUI(nil, nil, nil, &triggerSpy{}, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
		rec := postTrigger(t, ui, "daily", url.Values{"back": {back}})
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/runs/") {
			t.Errorf("back %q sent the reader to %q", back, loc)
		}
	}
}

// THE SECTION FOLLOWS ITSELF: the page polls its fragment every ten seconds,
// a fixed pace -- not the run page's, which reads "failed" in the text and
// would poll every two seconds in Portuguese.
func TestTheAttentionFollowsItself(t *testing.T) {
	ui, _ := attentionUI(t, failed{"daily", time.Minute, 1, "Error: x\n", nil})
	_, page := get(t, ui, "/")
	if !strings.Contains(page, `data-live="/overview/attention" data-live-every="10000"`) {
		t.Error("the attention does not follow itself every ten seconds")
	}
	code, frag := get(t, ui, "/overview/attention")
	if code != http.StatusOK || strings.Contains(frag, "<html") || !strings.Contains(frag, `data-live-target="attention"`) || !strings.Contains(frag, "data-attention-item=") {
		t.Errorf("the fragment answered %d: %.200s", code, frag)
	}
	_, js := get(t, ui, "/assets/ui.js")
	if !strings.Contains(js, "liveEvery") {
		t.Error("ui.js does not read data-live-every")
	}
}

// Each item can be rerun from here, with its run's values and the way back;
// one whose rerun is already going says so and links to it instead.
func TestAFailingWorkflowCanBeRerunAndSaysWhenItIs(t *testing.T) {
	ui, _ := attentionUI(t, failed{"daily", time.Minute, 1, "Error: x\n", nil}, failed{"hourly", time.Hour, 1, "Error: y\n", nil})
	_, body := get(t, ui, "/")
	att := attentionOf(t, body)
	item := att[strings.Index(att, `data-attention-item="daily"`):]
	item = item[:strings.Index(item, "</li>")]
	for _, want := range []string{`action="/workflows/daily/trigger"`, `name="back" value="/"`, ">Rerun<"} {
		if !strings.Contains(item, want) {
			t.Errorf("the item lacks %s", want)
		}
	}
}

// The rerun sends the failed run's values, as far as the workflow still
// declares them: a dropped param would have the trigger answer 400.
func TestTheRerunSendsTheFailedRunsValues(t *testing.T) {
	ui, _ := attentionUI(t, failed{"daily", time.Minute, 1, "Error: x\n", map[string]string{"region": "br", "gone": "x"}})
	_, body := get(t, ui, "/")
	item := attentionOf(t, body)
	if !strings.Contains(item, `name="param.region" value="br"`) || strings.Contains(item, "param.gone") {
		t.Errorf("the rerun does not send the values the workflow declares: %s", item)
	}
}

// A failing workflow whose rerun is already going says so, and links to it.
func TestARerunInFlightIsSaid(t *testing.T) {
	ui, _ := attentionUIWith(t, []postgres.RunSummary{{ID: "rerun-id", WorkflowSlug: "daily", Status: "running"}},
		failed{"daily", time.Minute, 1, "Error: x\n", nil})
	_, body := get(t, ui, "/")
	att := attentionOf(t, body)
	if !strings.Contains(att, `href="/runs/rerun-id"`) || !strings.Contains(att, "rerun in progress") || strings.Contains(att, "/trigger") {
		t.Error("a rerun in flight is not said, or another is offered")
	}
}

// A destination whose data stopped coming needs you too, with the way to its
// page in the catalog.
func TestALateTargetNeedsYou(t *testing.T) {
	late := []postgres.CatalogEntry{{
		Target: "postgres://db/public/orders", Kind: "postgres",
		Writers: []postgres.CatalogWriter{{Workflow: "orders_sync", Node: "load",
			LastLoaded: time.Now().Add(-90 * time.Minute), HasSchedule: true,
			Cron: "0 * * * *", Timezone: "UTC", Active: true}},
	}}
	ui := api.NewUI(overviewReads{asked: &struct {
		span  time.Duration
		grain postgres.Grain
		n     int
	}{}}, nil, nil, nil, nil, catalogFake{entries: late}, branding.Default(), slog.New(slog.DiscardHandler)).WithFailing(failingList{})
	_, body := get(t, ui, "/")
	att := attentionOf(t, body)
	for _, want := range []string{
		`data-attention-target="postgres://db/public/orders"`, "orders_sync missed",
		`href="/data/target?u=postgres%3A%2F%2Fdb%2Fpublic%2Forders"`, "Open in catalog", ">1<",
	} {
		if !strings.Contains(att, want) {
			t.Errorf("the attention lacks %s", want)
		}
	}
	if strings.Contains(att, "Nothing needs you right now") {
		t.Error("a late target reads as all is well")
	}
}
