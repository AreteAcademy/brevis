package pages

import (
	"slices"

	"github.com/AreteAcademy/brevis/internal/alerts"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
)

// RunPage is what the run page and its live refresh are drawn from. One
// struct, filled by one helper in the handler, so the page and the fragment
// that refreshes it cannot read a run differently.
type RunPage struct {
	Run    run.Run
	Logs   []postgres.StepLog
	Raised []alerts.Record
	Wait   *postgres.Wait

	// Graph is the embedded graph JSON (nav-latency NL5). Only the full page
	// carries it; the live fragment does not redraw the graph.
	Graph string

	// Step is a one-step run's step, drawn as a line where a multi-step run
	// has its graph (screen-run-v2 RV3). Nil for every other run.
	Step *StepLine

	// Workflow is the CURRENT definition, for Rerun: what the trigger resolves
	// against. Nil when the workflow is gone, and then there is no Rerun.
	Workflow *wf.Workflow
}

// RerunParams is the drawer's fields for a rerun: the workflow's params as it
// declares them NOW, each defaulting to the value this run used. A param
// declared since then keeps its own default. The names the run used that the
// definition no longer declares come back apart, sorted, because the trigger
// would refuse them and the operator should see what is not being sent.
//
// A copy: the workflow's own params are not touched.
func RerunParams(w wf.Workflow, used map[string]string) ([]wf.Param, []string) {
	params := make([]wf.Param, len(w.Params))
	declared := make(map[string]bool, len(w.Params))
	for i, p := range w.Params {
		declared[p.Name] = true
		if v, ok := used[p.Name]; ok {
			p.Default = v
		}
		params[i] = p
	}
	var dropped []string
	for name := range used {
		if !declared[name] {
			dropped = append(dropped, name)
		}
	}
	slices.Sort(dropped)
	return params, dropped
}

// rerunNote is said wherever a rerun is offered for a run with a slot. The
// rerun is a manual run: the auto params follow the moment it is clicked, and
// an operator redoing "the 04:00" has to know it will not read 04:00's window.
func rerunNote(r run.Run) string {
	if r.LogicalDate == nil {
		return ""
	}
	return "Runs now with these values. The auto params follow now, not slot " + components.UTCStamp(r.LogicalDate) + "."
}

// drawerText is what changes between the drawer's two uses: running a
// workflow, and rerunning one of its runs.
type drawerText struct {
	Title, Button, Note string
	Dropped             []string
}

var runDrawerText = drawerText{Title: "Run with parameters", Button: "Run"}

func rerunDrawerText(r run.Run, dropped []string) drawerText {
	return drawerText{Title: "Rerun with parameters", Button: "Rerun", Note: rerunNote(r), Dropped: dropped}
}

// rerunWorkflow is the workflow as the rerun drawer draws it: the same, with
// its params pre-filled from the run.
func rerunWorkflow(p RunPage) (wf.Workflow, []string) {
	w := *p.Workflow
	params, dropped := RerunParams(w, p.Run.Params)
	w.Params = params
	return w, dropped
}
