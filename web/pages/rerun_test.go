package pages

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// A RERUN STARTS FROM WHAT THE RUN USED (screen-run-v2 RV2): each param the
// workflow still declares comes pre-filled with this run's value; one declared
// since then keeps its default; one the definition dropped is not sent, and is
// named so the operator knows it is gone.
func TestRerunParamsKeepTheRunsValues(t *testing.T) {
	w := wf.Workflow{Slug: "daily", Params: []wf.Param{
		{Name: "date", Type: wf.ParamString, Default: "2026-01-01"},
		{Name: "full", Type: wf.ParamBool, Default: "false"},
		{Name: "added", Type: wf.ParamString, Default: "new"},
	}}
	used := map[string]string{"date": "2026-10-15", "full": "true", "gone": "x", "also_gone": "y"}

	params, dropped := RerunParams(w, used)
	got := map[string]string{}
	for _, p := range params {
		got[p.Name] = p.Default
	}
	if got["date"] != "2026-10-15" || got["full"] != "true" || got["added"] != "new" || len(params) != 3 {
		t.Errorf("params = %v", got)
	}
	if !slices.Equal(dropped, []string{"also_gone", "gone"}) {
		t.Errorf("dropped = %v, want them named and sorted", dropped)
	}
	if w.Params[0].Default != "2026-01-01" {
		t.Error("RerunParams changed the workflow's own params")
	}
}

func rerunPage(r run.Run, w *wf.Workflow) string {
	var b strings.Builder
	_ = Run(RunPage{Run: r, Workflow: w}).Render(context.Background(), &b)
	return b.String()
}

// THE RERUN BUTTON, BY CASE: gone with the workflow, one click with no params,
// the drawer pre-filled with them -- and a run with a slot says that a rerun
// reads now's window, not the slot's.
func TestTheRerunActionByCase(t *testing.T) {
	r := run.Run{ID: uuid.New(), Status: run.StatusFailed, WorkflowSlug: "daily", CreatedAt: created,
		Params: map[string]string{"date": "2026-10-15", "gone": "x"}}

	if page := rerunPage(r, nil); strings.Contains(page, "data-rerun") {
		t.Error("a run whose workflow is gone offers a rerun")
	}

	plain := rerunPage(r, &wf.Workflow{Slug: "daily"})
	if !strings.Contains(plain, `data-rerun`) || !strings.Contains(plain, `action="/workflows/daily/trigger"`) || !strings.Contains(plain, ">Rerun<") && !strings.Contains(plain, "Rerun\n") {
		t.Error("a workflow with no params does not rerun in one click")
	}
	if strings.Contains(plain, `id="run"`) {
		t.Error("a workflow with no params draws a drawer")
	}

	with := rerunPage(r, &wf.Workflow{Slug: "daily", Params: []wf.Param{{Name: "date", Type: wf.ParamString}}})
	if !strings.Contains(with, `data-rerun`) || !strings.Contains(with, `data-dialogo="run"`) || !strings.Contains(with, "data-no-prefetch") {
		t.Error("a workflow with params does not open the drawer")
	}
	if !strings.Contains(with, "Rerun with parameters") || !strings.Contains(with, `value="2026-10-15"`) {
		t.Error("the drawer is not the rerun's, pre-filled")
	}
	if !strings.Contains(with, "gone") || !strings.Contains(with, "not in the current definition") {
		t.Error("the dropped param is not named")
	}
	if strings.Contains(with, "Runs now with these values") {
		t.Error("a run with no slot carries the window note")
	}

	slot := created
	r.LogicalDate = &slot
	slotted := rerunPage(r, &wf.Workflow{Slug: "daily", Params: []wf.Param{{Name: "date", Type: wf.ParamString}}})
	if !strings.Contains(slotted, "Runs now with these values") || !strings.Contains(slotted, "2026-10-15 04:00:00 UTC") {
		t.Error("a run with a slot does not say a rerun reads now's window")
	}
}
