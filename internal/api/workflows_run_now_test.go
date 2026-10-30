package api_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func runNowUI(spy *triggerSpy) *api.UI {
	reads := readsFake{workflows: []postgres.WorkflowSummary{
		{Slug: "daily", Name: "daily", Project: "p", Cron: "0 9 * * *", Timezone: "UTC", HasSchedule: true, Active: true},
		{Slug: "handmade", Name: "handmade", Project: "p"},
		{Slug: "tuned", Name: "tuned", Project: "p", Cron: "0 9 * * *", HasSchedule: true, Active: true},
	}}
	one, every := 0, 0
	defs := mapDefs{one: &one, every: &every, defs: map[string]wf.Workflow{
		"daily":    {Slug: "daily", Schedule: "0 9 * * *", Nodes: []wf.Node{{ID: "a", Run: "echo"}}},
		"handmade": {Slug: "handmade", Nodes: []wf.Node{{ID: "a", Run: "echo"}}},
		"tuned":    {Slug: "tuned", Schedule: "0 9 * * *", Params: []wf.Param{{Name: "full", Type: wf.ParamBool, Default: "false"}}, Nodes: []wf.Node{{ID: "a", Run: "echo"}}},
	}}
	return api.NewUI(reads, defs, nil, spy, nil, catalogFake{}, branding.Default(), slog.New(slog.DiscardHandler))
}

// dialogOf is the <dialog> with that id.
func dialogOf(t *testing.T, body, id string) string {
	t.Helper()
	at := strings.Index(body, `<dialog id="`+id+`"`)
	if at < 0 {
		t.Fatalf("there is no dialog %s", id)
	}
	return body[at : at+strings.Index(body[at:], "</dialog>")]
}

// RUN ASKS WHEN, WHEN THERE IS A WHEN: a workflow with no parameters opens
// "Run it now", which offers its schedule's last fourteen slots and a time of
// one's own; a workflow with parameters goes to its drawer; a manual-only one
// has no slot to offer. With no script, ▶ still posts the trigger.
func TestRunAsksWhenThereIsAWhen(t *testing.T) {
	_, body := get(t, runNowUI(&triggerSpy{}), "/workflows")

	daily := rowOf(t, body, "daily")
	if !strings.Contains(daily, `action="/workflows/daily/trigger"`) || !strings.Contains(daily, `data-dialogo="run-now-daily"`) {
		t.Error("daily's ▶ neither posts nor opens its dialog")
	}
	d := dialogOf(t, body, "run-now-daily")
	if n := strings.Count(d, `<option value="20`); n != 14 {
		t.Errorf("the dialog offers %d slots, want 14", n)
	}
	for _, want := range []string{`name="when" value="now"`, `name="when" value="slot"`, `value="other"`, `type="datetime-local" name="slot_at"`, "data-run-error"} {
		if !strings.Contains(d, want) {
			t.Errorf("the dialog has no %s", want)
		}
	}
	if !strings.Contains(d, "processes") {
		t.Error("a slot does not say the window it processes")
	}

	if d := dialogOf(t, body, "run-now-handmade"); strings.Contains(d, `value="slot"`) {
		t.Error("a manual-only workflow offers a slot")
	}
	_, js := get(t, runNowUI(&triggerSpy{}), "/assets/ui.js")
	if !strings.Contains(strip(js), `form[data-run-now]`) {
		t.Error("ui.js does not post the Run dialog")
	}
	if tuned := rowOf(t, body, "tuned"); !strings.Contains(tuned, `href="/workflows/tuned#run"`) || strings.Contains(tuned, "run-now-tuned") {
		t.Error("a workflow with parameters does not go to its drawer")
	}
}

func postRun(t *testing.T, ui *api.UI, form url.Values, script bool) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	r := httptest.NewRequest(http.MethodPost, "/workflows/daily/trigger", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if script {
		r.Header.Set("X-Brevis-Fragment", "trigger")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// THE DIALOG'S CHOICE REACHES THE SCHEDULER: now is no slot, a listed slot
// is that slot, and "another" is the time typed beside it.
func TestTheRunDialogsChoiceReachesTheScheduler(t *testing.T) {
	for _, c := range []struct {
		name string
		form url.Values
		want *time.Time
	}{
		{"now", url.Values{"when": {"now"}, "slot": {"2026-10-26T09:00:00Z"}}, nil},
		{"a listed slot", url.Values{"when": {"slot"}, "slot": {"2026-10-26T09:00:00Z"}}, ptr(time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC))},
		{"another", url.Values{"when": {"slot"}, "slot": {"other"}, "slot_at": {"2026-10-20T09:00"}}, ptr(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))},
	} {
		t.Run(c.name, func(t *testing.T) {
			spy := &triggerSpy{}
			if rec := postRun(t, runNowUI(spy), c.form, false); rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			if (spy.slot == nil) != (c.want == nil) || (c.want != nil && !spy.slot.Equal(*c.want)) {
				t.Errorf("slot = %v, want %v", spy.slot, c.want)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }

// THE DIALOG STAYS OPEN ON A REFUSAL: with the script, a refused slot is the
// scheduler's sentence for the dialog to show; a run made is where to go
// next, with the toast that says so waiting in the flash.
func TestTheRunDialogHearsTheOutcome(t *testing.T) {
	spy := &triggerSpy{err: errors.New(`2026-10-26 10:00 UTC is not a slot of "0 9 * * *"`)}
	rec := postRun(t, runNowUI(spy), url.Values{"when": {"slot"}, "slot": {"2026-10-26T10:00:00Z"}}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is not a slot") {
		t.Errorf("a refusal answered %d: %s", rec.Code, rec.Body)
	}

	rec = postRun(t, runNowUI(&triggerSpy{}), url.Values{"when": {"now"}, "back": {"/workflows?q=d"}}, true)
	var got struct{ Location string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Location != "/workflows?q=d" {
		t.Errorf("a run made answered %q (%v)", rec.Body, err)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "brevis_flash") {
		t.Error("the list is not told the run was made")
	}
}
