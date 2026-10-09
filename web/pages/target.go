package pages

import (
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
	"github.com/AreteAcademy/brevis/web/components"
)

// missedShown bounds the dashed bars after the last load: enough to see a
// pipeline stopped, not ten thousand ticks of a minutely one stopped for a week.
const missedShown = 6

// TargetView is one destination's page.
type TargetView struct {
	Row DataRow

	// Bars are the last loads, oldest first, each linking to its run; Missed
	// are the slots that went by since, past their grace, drawn dashed.
	Bars   []components.LoadBar
	Missed int

	// Loads are the same loads, newest first, for the table.
	Loads []postgres.TargetLoad

	// Preview is the first rows of the destination, when a SQL service
	// answered. Nil when there is none to ask, or when asking failed.
	Preview *sqlserve.Result

	// Tabs says there is a SQL service to ask at all. Without one there are
	// no tabs: a tab that always answers "not configured" is a question
	// nobody can act on.
	Tabs bool

	// Tab is which one is open: "", "preview" or "query". A query parameter,
	// so the link is shareable the way /data's filters already are.
	Tab string

	// Statement is what is in the Query box -- ECHOED BACK after a run,
	// because a query refused for a typo with the box emptied is a query
	// somebody has to type again to fix.
	Statement string

	// Query is what the statement returned, and QueryErr the reason there is
	// nothing. Both nil and empty until somebody runs one: opening the tab
	// asks nothing, which is the difference between a Query tab and a
	// preview with a text box.
	Query    *sqlserve.Result
	QueryErr string

	// PreviewErr is why there are no rows, in words somebody can act on.
	//
	// A STRING AND NOT AN error, because this is a view: whatever decides
	// which of a service's words may be repeated has already decided, and a
	// template is the wrong place to be making that judgement again.
	PreviewErr string
}

// HasPreview says whether there is anything to draw in the Preview panel.
//
// A REASON COUNTS. A destination the service refuses -- not a table, no
// connection for it -- still gets the panel, with the sentence where the grid
// would be; the alternative is a tab that opens onto nothing.
func (v TargetView) HasPreview() bool {
	return v.Preview != nil || v.PreviewErr != ""
}

// TabHref is the link to one tab, which is also the link somebody pastes
// into a message. The statement is NEVER in it: a query in a URL is a query
// in a proxy log, in a browser's history and in a Referer header, and a WHERE
// clause carries customer data. That is why the Query box is a POST.
func (v TargetView) TabHref(tab string) string {
	return "/data/target?u=" + url.QueryEscape(v.Row.Target) + "&tab=" + tab
}

// QueryNote is the line under the grid: what came back, what it cost, how
// long it took.
//
// THE COST IS ON SCREEN. A query tab that hides what a query scanned teaches
// nobody the difference between the query they wrote and the one that was
// cheap -- and the bill arrives either way.
func (v TargetView) QueryNote() string {
	if v.Query == nil {
		return ""
	}
	n := len(v.Query.Rows)
	rows := "rows"
	if n == 1 {
		rows = "row"
	}
	note := fmt.Sprintf("%d %s · %s scanned · %d ms", n, rows, bytesText(v.Query.Bytes), v.Query.Millis)
	if v.Query.Truncated {
		note += " — there are more"
	}
	return note
}

// bytesText is a byte count somebody can judge at a glance.
//
// A SECOND COPY OF `serve`'s, and deliberately: the engine does not import
// the sql module and never will -- that is what engine-weight.sh asserts --
// so the alternative to eight lines here is the number in full, which
// somebody would have to count the digits of.
func bytesText(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// PreviewNote is the line under the grid.
//
// IT SAYS WHEN IT CUT. The service goes to the trouble of asking for one row
// more than it shows so it can know; a page that dropped the fact would make
// that pointless, and somebody would read a MAX off the grid and be wrong.
func (v TargetView) PreviewNote() string {
	if v.Preview == nil {
		return ""
	}
	n := len(v.Preview.Rows)
	rows := "rows"
	if n == 1 {
		rows = "row"
	}
	if v.Preview.Truncated {
		return fmt.Sprintf("first %d %s — there are more", n, rows)
	}
	return fmt.Sprintf("%d %s", n, rows)
}

// Cell is one value, as a grid shows it.
//
// NULL IS NOT THE EMPTY STRING, and this is the last place that can still
// tell them apart: one is "nothing was recorded" and the other is a value
// somebody wrote. An em dash for the first, nothing for the second.
func Cell(v any) string {
	if v == nil {
		return "—"
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// BuildTarget judges the destination as the list does, and lays out its loads.
func BuildTarget(d postgres.TargetDetail, now time.Time) TargetView {
	v := TargetView{Row: BuildData([]postgres.CatalogEntry{d.CatalogEntry}, now).Rows[0], Loads: d.Loads}
	for _, l := range slices.Backward(d.Loads) {
		v.Bars = append(v.Bars, components.LoadBar{
			Href:  "/runs/" + l.RunID,
			Rows:  l.Rows,
			Title: fmt.Sprintf("%s · %s · %s", l.LoadedAt.UTC().Format("2006-01-02 15:04 UTC"), l.Workflow, rowsText(l.Rows)),
		})
	}
	// The slots the destination missed are its healthiest writer's: if any
	// writer is on time, nothing is missing.
	for _, w := range v.Row.Writers {
		if w.Verdict.Status == v.Row.Status {
			v.Missed = len(catalog.MissedSlots(writerOf(w.CatalogWriter), now, missedShown))
			break
		}
	}
	return v
}

// Reason is one sentence on why a writer stands where it does, in its own
// schedule's timezone.
func (w WriterView) Reason() string {
	if g := w.Gateway; g != nil {
		why := fmt.Sprintf("Published from gateway %s, stream %s (%s), on %s. It writes as events arrive; its traffic is on the gateway's /metrics.",
			g.Name, g.Stream, g.Role, g.PublishedAt.UTC().Format("Jan 2 15:04 UTC"))
		if g.Note != "" {
			why += " Unnamed: " + g.Note + "."
		}
		return why
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil || w.Timezone == "" {
		loc = time.UTC
	}
	stamp := func(t time.Time) string { return t.In(loc).Format("Jan 2 15:04 MST") }

	switch w.Verdict.Status {
	case catalog.Late, catalog.Stale:
		return fmt.Sprintf("%s missed its %s run. It was expected by %s: %s of grace, from how late it usually lands, never under 10 min.",
			w.Workflow, stamp(w.Verdict.Missed), stamp(w.Verdict.Missed.Add(w.Verdict.Grace)), w.Verdict.Grace)
	case catalog.Paused:
		return "Its schedule is paused, so it is never late."
	case catalog.Unscheduled:
		if w.HasSchedule {
			return "Its schedule cannot be read, so there is nothing to be late against."
		}
		return "It runs by hand, so there is nothing to be late against."
	}
	return ""
}

// shortID is the first block of a run id, enough to tell runs apart in a table.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
