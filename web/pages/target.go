package pages

import (
	"fmt"
	"slices"
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
