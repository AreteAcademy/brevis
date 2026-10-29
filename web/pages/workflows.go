package pages

import (
	"context"
	"fmt"
	"time"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// RecentRuns is how many of a workflow's last runs its row draws.
const RecentRuns = 14

// recentTitle is what a recent-run square says: its state and its time.
func recentTitle(r postgres.RecentRun) string {
	return r.Status + " · " + components.Timestamp(&r.At)
}

// zoneSuffix names a schedule's timezone after its words, when it is not
// UTC: "every day at 04:00" means four in São Paulo for a São Paulo cron.
func zoneSuffix(tz string) string {
	if tz == "" || tz == "UTC" || tz == "Etc/UTC" {
		return ""
	}
	return " (" + tz + ")"
}

// WorkflowCounts is how many workflows each filter choice would show, with
// the rest of the filter as it is. A choice's count ignores its own filter --
// "Failure 1" while "Success" is chosen -- or every other choice would read 0.
// The key "" is the choice that does not filter.
type WorkflowCounts struct {
	State  map[string]int
	Active map[string]int
}

// workflowStates are the state choices, in the order the filter shows them.
var workflowStates = []struct{ value, label string }{
	{"", "Any state"}, {"success", "Success"}, {"failed", "Failure"},
	{"running", "Running"}, {"queued", "Queued"},
}

// stateOptions is the state filter. Every choice stays, even at 0: a control
// whose choices come and go with the search moves under the pointer.
func stateOptions(f Filter, c WorkflowCounts) []components.SegOption {
	out := make([]components.SegOption, 0, len(workflowStates))
	for _, s := range workflowStates {
		out = append(out, components.SegOption{Label: s.label, Href: f.With("state", s.value),
			Count: fmt.Sprint(c.State[s.value]), Current: f.State == s.value})
	}
	return out
}

// scheduleOptions is the active/paused filter.
func scheduleOptions(f Filter, c WorkflowCounts) []components.SegOption {
	out := make([]components.SegOption, 0, 3)
	for _, s := range []struct{ value, label string }{
		{"", "Any schedule"}, {"active", "Active"}, {"paused", "Paused"},
	} {
		out = append(out, components.SegOption{Label: s.label, Href: f.With("active", s.value),
			Count: fmt.Sprint(c.Active[s.value]), Current: f.Active == s.value})
	}
	return out
}

// hidden is the filter the search form carries along, as name/value pairs:
// everything but the search itself and the page, which a new search resets.
// Only what is set, so a searched URL says no more than a clicked one.
func (f Filter) hidden() [][2]string {
	var out [][2]string
	for _, h := range [][2]string{
		{"state", f.State}, {"active", f.Active},
		{"project", f.Project}, {"sort", f.Sort},
	} {
		if h[1] != "" {
			out = append(out, h)
		}
	}
	for _, t := range f.Tags {
		out = append(out, [2]string{"tag", t})
	}
	if f.Desc {
		out = append(out, [2]string{"dir", "desc"})
	}
	return out
}

// tagFormHidden is what the tag popover's form carries along: the filter but
// the tags, which are its own checkboxes -- carried as hidden fields too,
// unchecking one would keep it.
func (f Filter) tagFormHidden() [][2]string {
	g := f
	g.Tags = nil
	out := g.hidden()
	if f.Search != "" {
		out = append([][2]string{{"q", f.Search}}, out...)
	}
	return out
}

// workflowStats are the same four numbers the dashboard opens with, scoped to
// one workflow -- and over thirty days rather than one, because a daily job
// has a single run in twenty-four hours and a rate over one sample is not a
// rate.
func workflowStats(st WorkflowStats) []components.Stat {
	i := st.Ind
	return []components.Stat{
		{Label: "Success rate", Value: pct(i.Ratio(i.Succeeded)),
			Note: fmt.Sprintf("%d of %d finished", i.Succeeded, i.Succeeded+i.Failed)},
		{Label: "Failure rate", Value: pct(i.Ratio(i.Failed)), Note: failureNote(i.Failed)},
		{Label: "Mean duration", Value: meanValue(i.MeanDuration),
			Note: fmt.Sprintf("over %d days", int(st.Window.Hours()/24))},
		{Label: "Open", Value: fmt.Sprint(i.Running + i.Pending), Note: openNote(i)},
	}
}

// scheduleZone is the schedule's timezone, when the summary was read.
func scheduleZone(sum *postgres.WorkflowSummary) string {
	if sum == nil {
		return ""
	}
	return sum.Timezone
}

// overviewKPIs are the Overview's four numbers (screen-overview T4): how much
// passed in the window, what failed and how many workflows still are failing,
// what runs and waits now, and what fires next. The retries the strip had
// stay, under the success rate.
func overviewKPIs(ctx context.Context, d OverviewData) []components.Stat {
	i := d.Ind
	finished := i.Succeeded + i.Failed
	success := components.Stat{Label: i18n.T(ctx, "overview.kpi.success"), Sub: windowLabel(ctx, d.Window), Dot: "bg-state-success", Value: "—"}
	failures := components.Stat{Label: i18n.T(ctx, "overview.kpi.failures"), Value: i18n.Number(ctx, int64(i.Failed))}
	if finished > 0 {
		success.Value = percent(ctx, i.Succeeded, finished)
		success.Note = i18n.T(ctx, "overview.kpi.of", "n", i18n.Number(ctx, int64(i.Succeeded)), "total", i18n.Number(ctx, int64(finished)))
		success.Bar = []components.BarSegment{{Share: float64(i.Succeeded) / float64(finished), Class: "bg-state-success"}}
		failures.Note = percent(ctx, i.Failed, finished)
	}
	if i.Retried > 0 {
		success.Foot = components.Text(i18n.N(ctx, "overview.kpi.retried", i.Retried))
	}
	if i.Failed > 0 {
		failures.Dot, failures.Tone = "bg-state-failed", "text-state-failed"
	}
	if n := len(d.Attention.Items) + d.Attention.More; n > 0 {
		failures.Foot = components.Link("/runs?state=failed", i18n.N(ctx, "overview.kpi.still_failing", n)+" →")
	}
	running := components.Stat{Label: i18n.T(ctx, "overview.kpi.running"), Dot: "bg-state-running",
		Value: i18n.Number(ctx, int64(i.Running)), Note: i18n.N(ctx, "overview.kpi.queued", d.Pending)}
	if i.MeanDuration > 0 {
		running.Foot = components.Text(i18n.T(ctx, "overview.kpi.mean", "span", i18n.Elapsed(ctx, i.MeanDuration)))
	}
	next := components.Stat{Label: i18n.T(ctx, "overview.kpi.next"), Value: i18n.T(ctx, "overview.kpi.no_schedule")}
	if len(d.Proximas) > 0 {
		p := d.Proximas[0]
		in := p.When.Sub(d.Now)
		next.Value, next.Note = i18n.T(ctx, "time.in", "span", untilText(in)), p.Workflow
		// Green when it is about to: the reader can watch it happen.
		if in < 2*time.Minute {
			next.Tone = "text-state-success"
		}
	}
	return []components.Stat{success, failures, running, next}
}

// percent is part of whole as the reader's language writes a percentage.
func percent(ctx context.Context, part, whole int) string {
	return i18n.Decimal(ctx, 100*float64(part)/float64(whole), 1) + "%"
}

// WorkflowPage is what a workflow's page is drawn from (screen-workflow-v2).
type WorkflowPage struct {
	Def wf.Workflow
	// Sum is the workflow's summary row: project, schedule state, the next
	// run. Nil when it could not be read; the page then says less.
	Sum    *postgres.WorkflowSummary
	Latest []postgres.RunSummary
	Stats  WorkflowStats
	// Graph is the embedded graph JSON; empty for a one-step workflow.
	Graph string
	// Step is a one-step workflow's step as a line, from its definition. Nil
	// when the graph draws it.
	Step *StepLine
	// Now is the page's clock, for "next run in 12m".
	Now time.Time
}

// NextRunText is when a workflow runs next, as its KPI cell says it
// (screen-workflow-v2 WV2): "in 12m" and the UTC time beside it while the
// schedule runs; "paused" or "manual" otherwise. From the server's clock, so
// it is as fresh as the page; nothing ticks.
func NextRunText(sum *postgres.WorkflowSummary, now time.Time) (value, sub string) {
	switch {
	case sum == nil:
		return "—", ""
	case !sum.HasSchedule:
		return "manual", ""
	case !sum.Active:
		return "paused", ""
	case sum.NextRun == nil:
		return "—", ""
	}
	return "in " + untilText(sum.NextRun.Sub(now)), sum.NextRun.UTC().Format("Jan 2 15:04") + " UTC"
}

// untilText is a wait rounded to what a person plans by: minutes under an
// hour, hours and minutes under two days, days after that.
func untilText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		if m := int(d.Minutes()) - h*60; m > 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// workflowPageStats are the four numbers and, fifth, when it runs next.
func workflowPageStats(p WorkflowPage) []components.Stat {
	value, sub := NextRunText(p.Sum, p.Now)
	return append(workflowStats(p.Stats), components.Stat{Label: "Next run", Value: value, Note: sub})
}
