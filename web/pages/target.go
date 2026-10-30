package pages

import (
	"context"
	"fmt"
	"github.com/AreteAcademy/brevis/web/i18n"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
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

	// Configs are how this destination's writers are configured, one per
	// writer that described itself.
	Configs []WriterConfigView

	// Tabs says there is a SQL service to ask about this destination at
	// all, for the link to the workbench. Without one there is no link: one
	// that always answers "not configured" is a question nobody can act on.
	Tabs bool

	// ReadBy are the published endpoints whose SQL reads this target: its
	// consumers, beside its writers.
	ReadBy []EndpointRef
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
	for _, w := range d.Writers {
		switch g := w.Gateway; {
		case g != nil && g.Config != nil:
			v.Configs = append(v.Configs, WriterConfigView{Gateway: g.Name, Stream: g.Stream,
				Role: g.Role, PublishedAt: g.PublishedAt, Described: true, Config: BuildConfig(g.Config),
				Flow: FlowOf(g.Config, d.Target)})
		case g == nil:
			c := WriterConfigView{Workflow: w.Workflow, Node: w.Node, Described: w.Config != nil}
			if w.Config != nil {
				c.Config = BuildConfig(w.Config)
				c.Flow = FlowOf(w.Config, d.Target)
			}
			v.Configs = append(v.Configs, c)
		}
	}
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
// schedule's timezone and the reader's language.
func (w WriterView) Reason(ctx context.Context) string {
	if g := w.Gateway; g != nil {
		why := i18n.T(ctx, "catalog.why.gateway", "gateway", g.Name, "stream", g.Stream, "role", g.Role,
			"at", i18n.Date(ctx, g.PublishedAt.UTC(), "brief")+" UTC")
		if g.Note != "" {
			why += " " + i18n.T(ctx, "catalog.why.unnamed", "note", g.Note)
		}
		return why
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil || w.Timezone == "" {
		loc = time.UTC
	}
	stamp := func(t time.Time) string { return i18n.Date(ctx, t.In(loc), "brief") + " " + t.In(loc).Format("MST") }

	switch w.Verdict.Status {
	case catalog.Late, catalog.Stale:
		grace := w.Verdict.Grace
		return i18n.T(ctx, "catalog.why.missed", "workflow", w.Workflow, "slot", stamp(w.Verdict.Missed),
			"by", stamp(w.Verdict.Missed.Add(grace)), "grace", components.Duration(&grace))
	case catalog.Paused:
		return i18n.T(ctx, "catalog.why.paused")
	case catalog.Unscheduled:
		if w.HasSchedule {
			return i18n.T(ctx, "catalog.why.unreadable")
		}
		return i18n.T(ctx, "catalog.why.manual")
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

// targetProperties is what the destination is, in the side column: its kind,
// where it lives and what it is, and the last load. Every value is a
// machine's, so every one is mono.
func targetProperties(v TargetView) []components.Def {
	kind := v.Row.Kind
	if v.Row.Legacy {
		kind = "legacy label"
	}
	if kind == "" {
		kind = "—"
	}
	where, what := SplitTarget(v.Row.Target)
	if where == "" {
		where = "—"
	}
	return []components.Def{
		{Term: "Kind", Value: kind, Mono: true},
		{Term: "Where", Value: where, Mono: true},
		{Term: "Object", Value: what, Mono: true},
		{Term: "Last loaded", Value: components.UTCStamp(v.Row.LastWhen), Mono: true},
		{Term: "Rows, last load", Value: rowsText(v.Row.LastRows), Mono: true},
	}
}
