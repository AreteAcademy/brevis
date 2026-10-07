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
)

// lastLoad is the destination's most recent load across its writers: when,
// and how many rows that load said it wrote.
func lastLoad(e postgres.CatalogEntry) (*time.Time, *int64) {
	var when *time.Time
	var rows *int64
	for i := range e.Writers {
		w := e.Writers[i]
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
}

// WriterView is one writer and its verdict.
type WriterView struct {
	postgres.CatalogWriter
	Verdict catalog.Verdict
}

// worstFirst is the order the page lists destinations in: what needs looking
// at, then what is fine, then what has no verdict.
var worstFirst = map[catalog.Status]int{
	catalog.Stale: 0, catalog.Late: 1, catalog.OnTime: 2, catalog.Paused: 3, catalog.Unscheduled: 4,
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
			verdict := catalog.Freshness(writerOf(w), now)
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
var statusOrder = []catalog.Status{catalog.Stale, catalog.Late, catalog.OnTime, catalog.Paused, catalog.Unscheduled}

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
}

// The values a filter may take. Anything else is ignored rather than turned
// into an empty page, the rule the workflows list applies to its own.
var (
	filterStatuses = map[string]bool{"attention": true, "on_time": true, "late": true,
		"stale": true, "paused": true, "unscheduled": true}
	filterKinds = map[string]bool{"bigquery": true, "postgres": true, "redshift": true,
		"mysql": true, "s3": true, "gs": true, "file": true, "pubsub": true}
)

// ParseDataFilter reads the filter from a query string.
func ParseDataFilter(q url.Values) DataFilter {
	f := DataFilter{Legacy: q.Get("legacy") == "1"}
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
	if len(parts) == 0 {
		return "/data"
	}
	return "/data?" + strings.Join(parts, "&")
}

// Active reports whether any field is set.
func (f DataFilter) Active() bool { return f.Status != "" || f.Kind != "" || f.Legacy }

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

// Apply narrows the rows to the filter. Total and Counts still describe every
// destination: the summary answers "how is the fleet", whatever is shown.
func (v DataView) Apply(f DataFilter) DataView {
	v.Filter = f
	rows := make([]DataRow, 0, len(v.Rows))
	for _, r := range v.Rows {
		if f.keeps(r) {
			rows = append(rows, r)
		}
	}
	v.Rows = rows
	return v
}
