package api_test

import (
	"context"
	"html"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// triggerSpy is the scheduler as the trigger sees it, keeping what it got.
type triggerSpy struct{ got map[string]string }

func (s *triggerSpy) Toggle(context.Context, string, string) (bool, error) { return false, nil }
func (s *triggerSpy) Disparar(_ context.Context, _ string, _ time.Time, params map[string]string) (uuid.UUID, error) {
	s.got = params
	return uuid.New(), nil
}

var (
	tagRE      = regexp.MustCompile(`(?s)<(input|select)\b([^>]*)>(.*?)(?:</select>|)`)
	attrRE     = regexp.MustCompile(`(\w[\w-]*)(?:="([^"]*)")?`)
	selectedRE = regexp.MustCompile(`<option value="([^"]*)" selected`)
)

// formFields reads a form the way a browser submits it, for the shapes the
// drawer draws: text and hidden inputs, ticked checkboxes, selects.
func formFields(t *testing.T, form string) url.Values {
	t.Helper()
	out := url.Values{}
	for _, m := range tagRE.FindAllStringSubmatch(form, -1) {
		attrs := map[string]string{}
		for _, a := range attrRE.FindAllStringSubmatch(m[2], -1) {
			attrs[a[1]] = html.UnescapeString(a[2])
		}
		name := attrs["name"]
		if name == "" {
			continue
		}
		if m[1] == "select" {
			body := form[strings.Index(form, m[0]):]
			body = body[:strings.Index(body, "</select>")]
			if s := selectedRE.FindStringSubmatch(body); s != nil {
				out.Add(name, s[1])
			}
			continue
		}
		if _, checked := attrs["checked"]; attrs["type"] == "checkbox" && !checked {
			continue
		}
		out.Add(name, attrs["value"])
	}
	return out
}

// A RERUN SENDS BACK WHAT THE RUN USED (screen-run-v2 RV2): the drawer is
// drawn from the run's values, and submitting it as a browser would gives the
// trigger the same values -- a string, a boolean, an enum, a list of ticks --
// minus the one the definition no longer declares.
func TestARerunPostsTheRunsValuesBack(t *testing.T) {
	w := wf.Workflow{Slug: "daily", Params: []wf.Param{
		{Name: "date", Type: wf.ParamString, Default: "2026-01-01"},
		{Name: "full", Type: wf.ParamBool, Default: "false"},
		{Name: "mode", Type: wf.ParamString, Enum: []string{"fast", "slow"}, Default: "fast"},
		{Name: "layers", Type: "list|string", Enum: []string{"bronze", "silver", "gold"}, Default: "bronze"},
		{Name: "tables", Type: "list|string", Default: "users"},
	}}
	used := map[string]string{"date": "2026-10-15", "full": "true", "mode": "slow",
		"layers": "bronze,gold", "tables": "users,orders", "gone": "x"}
	id := uuid.New()
	spy := &triggerSpy{}
	ui := api.NewUI(nil, defsFake{w: w}, execsFake{run: dom.Run{ID: id, WorkflowSlug: "daily", Status: dom.StatusFailed, Params: used}},
		spy, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))

	_, page := get(t, ui, "/runs/"+id.String())
	at := strings.Index(page, "data-run-form")
	if at < 0 {
		t.Fatal("the run page has no rerun drawer")
	}
	form := page[at : at+strings.Index(page[at:], "</form>")]
	if !strings.Contains(form, `action="/workflows/daily/trigger"`) && !strings.Contains(page[at-200:at], `action="/workflows/daily/trigger"`) {
		t.Error("the drawer does not post to the trigger")
	}

	mux := http.NewServeMux()
	ui.Registrar(mux)
	req := httptest.NewRequest(http.MethodPost, "/workflows/daily/trigger", strings.NewReader(formFields(t, form).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the trigger answered %d", rec.Code)
	}

	want := maps.Clone(used)
	delete(want, "gone")
	if !maps.Equal(spy.got, want) {
		t.Errorf("the rerun sent %v, want %v", spy.got, want)
	}
}
