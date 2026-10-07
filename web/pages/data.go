package pages

import (
	"fmt"
	"sort"
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
		v.Counts[row.Status]++
		v.Rows = append(v.Rows, row)
	}
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
