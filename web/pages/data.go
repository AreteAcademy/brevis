package pages

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/domain/schedule"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
)

// lastLoad is the destination's most recent load across its writers: when,
// and how many rows that load said it wrote.
func lastLoad(e postgres.CatalogEntry) (*time.Time, *int64) {
	var when *time.Time
	var rows *int64
	for i := range e.Writers {
		w := e.Writers[i]
		// A gateway measures nothing here; its traffic is on its /metrics.
		if w.Gateway != nil {
			continue
		}
		if when == nil || w.LastLoaded.After(*when) {
			when, rows = &e.Writers[i].LastLoaded, w.LastRows
		}
	}
	return when, rows
}

// rowsText renders a count, and says nothing for one the step did not give.
func rowsText(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprint(*v)
}

// DataView is /data as the template draws it: every destination with its
// verdict, worst first, and how many stand where.
type DataView struct {
	Rows   []DataRow
	Counts map[catalog.Status]int
	Total  int

	// Kinds present, sorted, and whether any legacy row exists: the filter bar
	// offers only what would match something.
	Kinds     []string
	HasLegacy bool

	Filter DataFilter
}

// DataRow is one destination. Writers shadows the entry's own list with each
// writer's verdict attached.
type DataRow struct {
	postgres.CatalogEntry
	Status   catalog.Status
	Writers  []WriterView
	LastWhen *time.Time
	LastRows *int64
	// Hidden is a row the search does not match. It is drawn, hidden: with
	// no script it is invisible, and the list filter can bring it back as
	// somebody deletes a letter.
	Hidden bool
}

// SearchText is what a search looks in, lower-cased -- the same text on the
// server and in the row's data-filter-text, so the two cannot disagree.
func (r DataRow) SearchText() string { return searchText(r) }

// WriterView is one writer and its verdict.
type WriterView struct {
	postgres.CatalogWriter
	Verdict catalog.Verdict
}

// worstFirst is the order the page lists destinations in: what needs looking
// at, then what is fine, then what has no verdict.
var worstFirst = map[catalog.Status]int{
	catalog.Stale: 0, catalog.Late: 1, catalog.OnTime: 2, catalog.Continuous: 3,
	catalog.Paused: 4, catalog.Unscheduled: 5, catalog.Unidentified: 6,
}

// BuildData judges every writer against its own schedule and every destination
// by its best writer -- data arriving from any writer is data arriving -- then
// orders the page worst first and, within a status, most recently loaded first.
//
// `now` is a parameter so the page is testable without a clock.
func BuildData(entries []postgres.CatalogEntry, now time.Time) DataView {
	v := DataView{Counts: map[catalog.Status]int{}, Total: len(entries)}
	for _, e := range entries {
		row := DataRow{CatalogEntry: e}
		row.LastWhen, row.LastRows = lastLoad(e)
		var statuses []catalog.Status
		for _, w := range e.Writers {
			verdict := verdictOf(e, w, now)
			row.Writers = append(row.Writers, WriterView{CatalogWriter: w, Verdict: verdict})
			statuses = append(statuses, verdict.Status)
		}
		row.Status = catalog.Best(statuses...)
		if e.Kind != "" && !slices.Contains(v.Kinds, e.Kind) {
			v.Kinds = append(v.Kinds, e.Kind)
		}
		v.HasLegacy = v.HasLegacy || e.Legacy
		v.Counts[row.Status]++
		v.Rows = append(v.Rows, row)
	}
	sort.Strings(v.Kinds)
	sort.SliceStable(v.Rows, func(i, j int) bool {
		a, b := v.Rows[i], v.Rows[j]
		if worstFirst[a.Status] != worstFirst[b.Status] {
			return worstFirst[a.Status] < worstFirst[b.Status]
		}
		if a.LastWhen == nil || b.LastWhen == nil {
			return b.LastWhen == nil && a.LastWhen != nil
		}
		return a.LastWhen.After(*b.LastWhen)
	})
	return v
}

// verdictOf judges one writer. A gateway stream is not judged against a
// schedule -- it writes as events arrive -- so it is continuous, or
// unidentified when its manifest could not name the destination.
func verdictOf(e postgres.CatalogEntry, w postgres.CatalogWriter, now time.Time) catalog.Verdict {
	if w.Gateway != nil {
		if e.Target == "" {
			return catalog.Verdict{Status: catalog.Unidentified}
		}
		return catalog.Verdict{Status: catalog.Continuous}
	}
	return catalog.Freshness(writerOf(w), now)
}

// writerOf is what freshness needs from a catalog row.
func writerOf(w postgres.CatalogWriter) catalog.Writer {
	out := catalog.Writer{Last: w.LastLoaded, Lag: w.Lag}
	if w.HasSchedule {
		out.Schedule = &schedule.Schedule{
			WorkflowSlug: w.Workflow, Cron: w.Cron, Timezone: w.Timezone, Active: w.Active,
		}
	}
	return out
}

// statusOrder is the order the summary lists statuses in.
var statusOrder = []catalog.Status{catalog.Stale, catalog.Late, catalog.OnTime, catalog.Continuous,
	catalog.Paused, catalog.Unscheduled, catalog.Unidentified}

// plural writes "1 destination" and "3 destinations".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// DataFilter narrows /data. It lives in the query string so a filtered page is
// a link somebody can send.
type DataFilter struct {
	// Status is "attention" (late or stale) or one status; "" is all.
	Status string
	// Kind is a target scheme; "" is all.
	Kind   string
	Legacy bool
	// Q searches the URI and the writers -- workflow, step, gateway and its
	// stream -- case-insensitively. "" is no search.
	Q string

	// Path is the page the filter's links go to; "" is /data. The loads list
	// filters the same way and keeps its links on itself.
	Path string
}

// searchCeiling bounds what a search may be: it is matched against every row,
// and nobody's destination name is longer than this.
const searchCeiling = 200

// The values a filter may take. Anything else is ignored rather than turned
// into an empty page, the rule the workflows list applies to its own.
var (
	filterStatuses = map[string]bool{"attention": true, "on_time": true, "late": true,
		"stale": true, "paused": true, "unscheduled": true, "continuous": true, "unidentified": true}
	filterKinds = map[string]bool{"bigquery": true, "postgres": true, "redshift": true,
		"mysql": true, "s3": true, "gs": true, "file": true, "pubsub": true}
)

// ParseDataFilter reads the filter from a query string.
func ParseDataFilter(q url.Values) DataFilter {
	f := DataFilter{Legacy: q.Get("legacy") == "1", Q: strings.TrimSpace(q.Get("q"))}
	if r := []rune(f.Q); len(r) > searchCeiling {
		f.Q = string(r[:searchCeiling])
	}
	if s := q.Get("status"); filterStatuses[s] {
		f.Status = s
	}
	if k := q.Get("kind"); filterKinds[k] {
		f.Kind = k
	}
	return f
}

// With is the link to this filter with one field changed. The fields are
// always written in the same order, so one filter has exactly one URL.
func (f DataFilter) With(field, value string) string {
	switch field {
	case "status":
		f.Status = value
	case "kind":
		f.Kind = value
	case "legacy":
		f.Legacy = value == "1"
	case "q":
		f.Q = value
	}
	var parts []string
	if f.Status != "" {
		parts = append(parts, "status="+url.QueryEscape(f.Status))
	}
	if f.Kind != "" {
		parts = append(parts, "kind="+url.QueryEscape(f.Kind))
	}
	if f.Legacy {
		parts = append(parts, "legacy=1")
	}
	if f.Q != "" {
		parts = append(parts, "q="+url.QueryEscape(f.Q))
	}
	base := f.Path
	if base == "" {
		base = "/data"
	}
	if len(parts) == 0 {
		return base
	}
	return base + "?" + strings.Join(parts, "&")
}

// Active reports whether any field is set.
func (f DataFilter) Active() bool { return f.Status != "" || f.Kind != "" || f.Legacy || f.Q != "" }

func (f DataFilter) keeps(r DataRow) bool {
	switch {
	case f.Status == "attention" && r.Status != catalog.Late && r.Status != catalog.Stale:
		return false
	case f.Status != "" && f.Status != "attention" && string(r.Status) != f.Status:
		return false
	case f.Kind != "" && r.Kind != f.Kind:
		return false
	case f.Legacy && !r.Legacy:
		return false
	}
	return true
}

// matches is the search alone: it hides a row rather than dropping it.
func (f DataFilter) matches(r DataRow) bool {
	return f.Q == "" || strings.Contains(searchText(r), strings.ToLower(f.Q))
}

// searchText is everything a search looks in, lower-cased: where the data
// lands and who writes it.
func searchText(r DataRow) string {
	parts := []string{r.Target}
	for _, w := range r.Writers {
		if g := w.Gateway; g != nil {
			parts = append(parts, g.Name, g.Stream)
		} else {
			parts = append(parts, w.Workflow, w.Node)
		}
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// SplitTarget cuts a URI into where and what -- `postgres://brevis_it/` and
// `public/landing_orders` -- so a table of them can dim the part every row
// shares. A legacy label (`bronze.payments`) splits at its last dot. Joined,
// the two halves are the target, character for character.
func SplitTarget(t string) (where, what string) {
	if i := strings.Index(t, "://"); i >= 0 {
		rest := t[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return t[:i+3+j+1], rest[j+1:]
		}
		return t, ""
	}
	if i := strings.LastIndex(t, "."); i >= 0 {
		return t[:i+1], t[i+1:]
	}
	return "", t
}

// kindIcon is the glyph a destination's kind is drawn with.
func kindIcon(kind string) string {
	switch kind {
	case "gs", "s3":
		return "cloud"
	case "file":
		return "folder"
	case "pubsub":
		return "plug"
	}
	return "database"
}

// statusOptions is the status filter: everything, what needs a person, then
// each status present with how many stand there.
func statusOptions(v DataView) []components.SegOption {
	f := v.Filter
	out := []components.SegOption{{Label: "All", Href: f.With("status", ""), Current: f.Status == ""}}
	if n := v.Counts[catalog.Late] + v.Counts[catalog.Stale]; n > 0 {
		out = append(out, components.SegOption{Label: "Attention", Href: f.With("status", "attention"),
			Count: fmt.Sprint(n), Current: f.Status == "attention"})
	}
	for _, st := range statusOrder {
		if n := v.Counts[st]; n > 0 {
			out = append(out, components.SegOption{Label: components.FreshnessLabel(string(st)),
				Href: f.With("status", string(st)), Count: fmt.Sprint(n), Current: f.Status == string(st)})
		}
	}
	return out
}

// kindOptions is the kind filter, offered only when there is a choice.
func kindOptions(v DataView) []components.SegOption {
	f := v.Filter
	out := []components.SegOption{{Label: "Any kind", Href: f.With("kind", ""), Current: f.Kind == ""}}
	for _, k := range v.Kinds {
		out = append(out, components.SegOption{Label: k, Href: f.With("kind", k), Current: f.Kind == k})
	}
	return out
}

// legacyOptions is the label filter, offered only when a legacy row exists.
func legacyOptions(v DataView) []components.SegOption {
	f := v.Filter
	return []components.SegOption{
		{Label: "Any label", Href: f.With("legacy", ""), Current: !f.Legacy},
		{Label: "Legacy only", Href: f.With("legacy", "1"), Current: f.Legacy},
	}
}

// Apply narrows the rows to the filter. Total and Counts still describe every
// destination: the summary answers "how is the fleet", whatever is shown.
func (v DataView) Apply(f DataFilter) DataView {
	v.Filter = f
	rows := make([]DataRow, 0, len(v.Rows))
	for _, r := range v.Rows {
		if f.keeps(r) {
			r.Hidden = !f.matches(r)
			rows = append(rows, r)
		}
	}
	v.Rows = rows
	return v
}

// Shown is how many rows are drawn visible.
func (v DataView) Shown() int {
	n := 0
	for _, r := range v.Rows {
		if !r.Hidden {
			n++
		}
	}
	return n
}
