package pages

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// RecentRuns is how many of a workflow's last runs its row draws.
const RecentRuns = 20

// recentTitle is what a recent-run square says: its state and its time.
func recentTitle(ctx context.Context, r postgres.RecentRun) string {
	return components.StateWord(ctx, r.Status) + " · " + components.Timestamp(&r.At)
}

// zoneSuffix names a schedule's timezone after its words, when it is not
// UTC: "every day at 04:00" means four in São Paulo for a São Paulo cron.
func zoneSuffix(tz string) string {
	if tz == "" || tz == "UTC" || tz == "Etc/UTC" {
		return ""
	}
	return " (" + tz + ")"
}

// WorkflowsScreen is everything the Workflows list draws.
type WorkflowsScreen struct {
	// Rows are the page's workflows, filtered and sorted.
	Rows   []postgres.WorkflowSummary
	Filter Filter
	Counts WorkflowCounts
	// Tags are every workflow's tags, for the popover; Langs what they run.
	Tags  []string
	Langs []LangChoice
	// Recent are each row's last runs.
	Recent map[string][]postgres.RecentRun
	// RunsIn is what each row runs, every language and tool; Connects what
	// it talks to; Steps how many steps; Failing whether its last finished
	// run failed. By slug.
	RunsIn   map[string][]string
	Connects map[string][]string
	Steps    map[string]int
	Failing  map[string]bool
	// Params is whether each row declares parameters, which its Run asks
	// for in the workflow's drawer.
	Params map[string]bool
	// Now is the instant the page is drawn at: the Run dialogs' last slots
	// are up to it.
	Now time.Time
	// Total is every workflow; Filtered those the filter keeps.
	Total, Filtered int
}

// LangChoice is one language of the filter: a logo key, and how many
// workflows the rest of the filter keeps that run it.
type LangChoice struct {
	Key   string
	Count int
}

// WorkflowCounts is how many workflows each state would show, with the rest
// of the filter as it is. A choice's count ignores its own filter --
// "Failing 1" while "Paused" is chosen -- or every other choice would read 0.
// The key "" is the choice that does not filter.
type WorkflowCounts struct {
	State map[string]int
}

// workflowStates are the questions the state segment asks, in its order.
var workflowStates = []struct{ value, key string }{
	{"", "workflows.state.all"},
	{"failing", "workflows.state.failing"},
	{"running", "workflows.state.running"},
	{"paused", "workflows.state.paused"},
	{"manual", "workflows.state.manual"},
}

// IsWorkflowState says whether a state is one of the segment's questions;
// any other is a run state an older link carries.
func IsWorkflowState(s string) bool {
	for _, st := range workflowStates {
		if st.value == s {
			return true
		}
	}
	return false
}

// stateOptions is the state segment. Every choice stays, even at 0: a control
// whose choices come and go with the search moves under the pointer.
func stateOptions(ctx context.Context, f Filter, c WorkflowCounts) []components.SegOption {
	out := make([]components.SegOption, 0, len(workflowStates))
	for _, s := range workflowStates {
		out = append(out, components.SegOption{Label: i18n.T(ctx, s.key), Href: f.With("state", s.value),
			Count: i18n.Number(ctx, int64(c.State[s.value])), Current: f.State == s.value})
	}
	return out
}

// legacyChips are the filters an older link carries that no control offers
// any more: a run state, and the schedule filter. Each still filters, and is
// shown with its own ×.
func legacyChips(ctx context.Context, f Filter) [][3]string {
	var out [][3]string
	if f.State != "" && !IsWorkflowState(f.State) {
		out = append(out, [3]string{i18n.T(ctx, "workflows.chip.last_run"), components.StateWord(ctx, f.State), f.With("state", "")})
	}
	switch f.Active {
	case "active":
		out = append(out, [3]string{i18n.T(ctx, "workflows.chip.schedule"), i18n.T(ctx, "workflows.active"), f.With("active", "")})
	case "paused":
		out = append(out, [3]string{i18n.T(ctx, "workflows.chip.schedule"), i18n.T(ctx, "workflows.paused"), f.With("active", "")})
	}
	return out
}

// hidden is the filter the search form carries along, as name/value pairs:
// everything but the search itself and the page, which a new search resets.
// Only what is set, so a searched URL says no more than a clicked one.
func (f Filter) hidden() [][2]string {
	var out [][2]string
	for _, h := range [][2]string{
		{"state", f.State}, {"active", f.Active}, {"lang", f.Lang},
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
	// Langs is every language and tool the workflow runs, as logo keys.
	Langs []string
	// Day narrows the Runs tab to one UTC day -- a square of the calendar;
	// nil is the last runs.
	Day *time.Time
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

// nextRunText is when a schedule fires next, rounded to what a person plans
// by -- "in 9h 28m" -- with the UTC time under it. Computed on the server;
// nothing ticks.
func nextRunText(ctx context.Context, next, now time.Time) (value, sub string) {
	return i18n.T(ctx, "time.in", "span", untilText(next.Sub(now))), i18n.Date(ctx, next.UTC(), "brief") + " UTC"
}

// kpiDays is the window of the workflow page's numbers.
const kpiDays = 30

// workflowPageStats are the workflow's five numbers over thirty days
// (screen-workflows T5): how many of the days it ran had no failure, how many
// had one, how long a run takes, what runs now, and when it fires next. Days,
// not runs: a */5 that failed once among 288 runs had a bad day, and a rate
// over runs would call it 99.7%.
func workflowPageStats(ctx context.Context, p WorkflowPage) []components.Stat {
	ran, failed := 0, 0
	since := p.Now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(kpiDays - 1))
	for _, d := range p.Stats.Days {
		if d.Date.Before(since) || d.Total == 0 {
			continue
		}
		ran++
		if d.Failed > 0 {
			failed++
		}
	}
	last30 := i18n.T(ctx, "workflow.kpi.last_days", "days", i18n.Number(ctx, kpiDays))
	clean := components.Stat{Label: i18n.T(ctx, "workflow.kpi.clean"), Dot: "bg-state-success", Value: "—", Note: last30}
	if ran > 0 {
		clean.Value = percent(ctx, ran-failed, ran)
		clean.Note = i18n.T(ctx, "workflow.kpi.of_days", "n", i18n.Number(ctx, int64(ran-failed)), "total", i18n.Number(ctx, int64(ran)))
		clean.Bar = []components.BarSegment{{Share: float64(ran-failed) / float64(ran), Class: "bg-state-success"}}
	}
	i := p.Stats.Ind
	open := components.Stat{Label: i18n.T(ctx, "workflow.kpi.open"), Value: i18n.Number(ctx, int64(i.Running+i.Pending)),
		Note: i18n.T(ctx, "workflow.kpi.nothing_running")}
	if i.Running+i.Pending > 0 {
		open.Note = i18n.T(ctx, "workflow.kpi.open_note", "running", i18n.Number(ctx, int64(i.Running)), "queued", i18n.Number(ctx, int64(i.Pending)))
	}
	next := components.Stat{Label: i18n.T(ctx, "workflow.kpi.next"), Value: "—"}
	switch sum := p.Sum; {
	case sum == nil:
	case !sum.HasSchedule:
		next.Value = i18n.T(ctx, "workflows.manual_only")
	case !sum.Active:
		next.Value = i18n.T(ctx, "workflows.paused")
	case sum.NextRun != nil:
		next.Value, next.Note = nextRunText(ctx, *sum.NextRun, p.Now)
	}
	return []components.Stat{
		clean,
		{Label: i18n.T(ctx, "workflow.kpi.failed_days"), Dot: "bg-state-failed", Value: i18n.Number(ctx, int64(failed)), Note: last30},
		{Label: i18n.T(ctx, "workflow.kpi.mean"), Value: meanValue(i.MeanDuration), Note: last30},
		open,
		next,
	}
}

// triggerOf is a run's trigger as the Runs tab says it: a manual run of a
// past slot is not a click like any other.
func triggerOf(ctx context.Context, r postgres.RunSummary) string {
	if r.TriggerType == "manual" && r.LogicalDate != nil {
		return i18n.T(ctx, "workflow.runs.manual_slot")
	}
	return triggerWord(ctx, r.TriggerType)
}

// givenParams are the values a run was given that differ from the current
// defaults, as key=value, sorted: what made it different. A value equal to
// its default is not news, and one the definition no longer declares still
// is.
func givenParams(w wf.Workflow, given map[string]string) []string {
	defaults := map[string]string{}
	for _, p := range w.Params {
		defaults[p.Name] = p.Default
	}
	var out []string
	for k, v := range given {
		if d, ok := defaults[k]; ok && d == v {
			continue
		}
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// workflowTabsCSS shows the workflow page's tabs by the hash, before the
// first paint.
func workflowTabsCSS() string { return hashTabsCSS("workflow-tabs", "runs", "params") }

// langsShown is how many marks the language column draws; the rest are a
// count.
const langsShown = 4

// firstLangs is the marks the language column draws.
func firstLangs(keys []string) []string {
	return keys[:min(len(keys), langsShown)]
}

// langText names what a workflow runs: the first two by name, and how many
// more -- "Go + Python +2".
func langText(ctx context.Context, keys []string) string {
	names := make([]string, 0, 2)
	for _, k := range keys[:min(len(keys), 2)] {
		names = append(names, components.LogoTitle(ctx, k))
	}
	out := strings.Join(names, " + ")
	if more := len(keys) - 2; more > 0 {
		out += " +" + i18n.Number(ctx, int64(more))
	}
	return out
}

// inFlight is whether the workflow's last run has not finished.
func inFlight(w postgres.WorkflowSummary) bool {
	return w.LastStatus == "running" || w.LastStatus == "queued" || w.LastStatus == "retrying"
}

// rowHref is the workflow's page.
func rowHref(slug string) string { return "/workflows/" + url.PathEscape(slug) }

// createExample is the smallest workflow worth publishing, for the drawer.
const createExample = `name: daily_orders
schedule: "0 4 * * *"
steps:
  - id: load
    run: python load.py
  - id: build
    run: dbt build --select orders+`

// RunNow is what the Run dialog needs: the workflow, its schedule, and the
// page to come back to.
type RunNow struct {
	Slug, Cron, Zone string
	Back             string
	Now              time.Time
}

// recentSlotsShown is how many past slots the Run dialog offers.
const recentSlotsShown = 14

func runNowID(slug string) string { return "run-now-" + slug }

// slotLabel is a slot as the Run dialog lists it: when, in the schedule's
// zone, and the window it processes, in UTC like every auto param.
func slotLabel(ctx context.Context, s Slot, zone string) string {
	at := s.At
	if loc, err := time.LoadLocation(zone); err == nil && zone != "" {
		at = at.In(loc)
	}
	return i18n.T(ctx, "workflows.slot.option",
		"slot", i18n.Date(ctx, at, "brief"),
		"from", i18n.Date(ctx, s.From, "brief"), "to", i18n.Date(ctx, s.At, "brief"))
}

// zoneName is a schedule's zone, UTC when it names none.
func zoneName(zone string) string {
	if zone == "" {
		return "UTC"
	}
	return zone
}
