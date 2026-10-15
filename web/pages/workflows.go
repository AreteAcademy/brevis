package pages

import (
	"fmt"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
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
