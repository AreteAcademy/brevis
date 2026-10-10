package pages

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
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

// NoConnectionFor is the sentence a destination gets when nothing is
// declared for it.
//
// IT NAMES THE DATABASE AND THE FILE. `serve` answers "no connection is
// declared for that destination" and names nothing on purpose -- its
// refusals never echo their input. This page already draws the target at the
// top, so it can say which, and a reader who is told only "no connection"
// has to go and work out which one.
func NoConnectionFor(target string) string {
	first := target
	if _, rest, ok := strings.Cut(target, "://"); ok {
		first, _, _ = strings.Cut(rest, "/")
	}
	return fmt.Sprintf("No connection is declared for %q. Add it to brevis.yaml "+
		"beside the SQL service and restart it.", first)
}

// WorkbenchHref opens `/sql` on this destination.
//
// A TARGET IN A URL IS FINE AND A STATEMENT IS NOT, which is the distinction
// the workbench's POST keeps: this console already puts a target in
// `/data/target?u=`, and a query in a link would be a query in a proxy log.
func (v TargetView) WorkbenchHref() string {
	return "/sql?target=" + url.QueryEscape(v.Row.Target)
}

// TabHref is the link to one tab, which is also the link somebody pastes
// into a message. The statement is NEVER in it: a query in a URL is a query
// in a proxy log, in a browser's history and in a Referer header, and a WHERE
// clause carries customer data. That is why the Query box is a POST.
func (v TargetView) TabHref(tab string) string {
	return "/data/target?u=" + url.QueryEscape(v.Row.Target) + "&tab=" + tab
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
		// THE WORD, AND NOT A DASH. Both distinguish a NULL from an empty
		// string, which is what this has always been for; a dash makes the
		// reader guess which of the two it is, and in a column of text it
		// could be a value somebody wrote. `null` is what the warehouse
		// calls it and what every console reading one prints.
		return "null"
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// Numeric says a cell should line up as a number, read FROM WHAT IT WILL
// SAY and not from its Go type.
//
// MEASURED AGAINST BOTH WAREHOUSES. BigQuery's REST API returns every scalar
// as a JSON string -- `SELECT 1, 1.5, true, NULL, "txt"` comes back
// `["1", "1.5", "true", null, "txt"]`, asked of the live API -- while
// Postgres hands over typed values that cross the wire as JSON numbers. A
// rule reading the Go type would line up one warehouse's integers and leave
// the other's ragged, for the same query against the same data.
//
// So a string of digits lines up, which is also what a spreadsheet does with
// one. The cost is an identifier made of digits lining up too, and that is
// the right answer in a grid anyway.
func Numeric(v any) bool {
	s := Cell(v)
	if s == "" || v == nil {
		return false
	}
	// `ParseFloat` accepts "Inf", "NaN" and "1e9"; the first two are words
	// in a text column far more often than they are numbers.
	switch s[0] {
	case '-', '+', '.', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
	default:
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// Total is what the pager may claim, which is not always a count.
//
// A LIMIT MAKES THE ROWS IN HAND A FLOOR. "1-50 of 500" is a sentence
// somebody reads a MAX off and is wrong about; the `+` is the same fact
// without the lie, and it is the same reason the note under the grid has
// always ended "— there are more".
func Total(rows int, truncated bool) string {
	n := strconv.Itoa(rows)
	if truncated {
		return n + "+"
	}
	return n
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
