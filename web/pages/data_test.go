package pages

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

var now = time.Date(2026, 10, 7, 12, 30, 0, 0, time.UTC)

func hourly(workflow string, last time.Time) postgres.CatalogWriter {
	return postgres.CatalogWriter{Workflow: workflow, Node: "load", LastLoaded: last,
		HasSchedule: true, Cron: "0 * * * *", Timezone: "UTC", Active: true}
}

func entry(target string, ws ...postgres.CatalogWriter) postgres.CatalogEntry {
	return postgres.CatalogEntry{Target: target, Kind: "postgres", Writers: ws}
}

func TestEachDestinationTakesItsBestWritersStatusAndProblemsComeFirst(t *testing.T) {
	paused := hourly("paused_job", now.Add(-72*time.Hour))
	paused.Active = false
	manual := postgres.CatalogWriter{Workflow: "by_hand", Node: "load", LastLoaded: now.Add(-24 * time.Hour)}

	d := BuildData([]postgres.CatalogEntry{
		entry("postgres://db/public/fresh", hourly("a", now.Add(-20*time.Minute))),
		entry("postgres://db/public/stale", hourly("b", now.Add(-5*time.Hour))),
		entry("postgres://db/public/late", hourly("c", now.Add(-85*time.Minute))),
		entry("postgres://db/public/manual", manual),
		entry("postgres://db/public/paused", paused),
		// Two writers: one stale, one fresh. Data arriving from any writer is
		// data arriving, so the destination is on time.
		entry("postgres://db/public/two", hourly("old", now.Add(-9*time.Hour)), hourly("new", now.Add(-10*time.Minute))),
	}, now)

	got := []string{}
	for _, r := range d.Rows {
		got = append(got, r.Target+"="+string(r.Status))
	}
	want := []string{
		"postgres://db/public/stale=stale",
		"postgres://db/public/late=late",
		"postgres://db/public/two=on_time", // last loaded 10 min ago
		"postgres://db/public/fresh=on_time",
		"postgres://db/public/paused=paused",
		"postgres://db/public/manual=unscheduled",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %s, want %s\nall: %v", i, got[i], want[i], got)
		}
	}

	if d.Counts[catalog.OnTime] != 2 || d.Counts[catalog.Stale] != 1 || d.Total != 6 {
		t.Errorf("counts = %v, total %d", d.Counts, d.Total)
	}

	// Each writer keeps its own verdict, so the page can say which one fell behind.
	two := d.Rows[2]
	if two.Writers[0].Verdict.Status != catalog.Stale || two.Writers[1].Verdict.Status != catalog.OnTime {
		t.Errorf("writer verdicts = %+v", two.Writers)
	}
}

func TestALateWriterWithARunInFlightSaysSo(t *testing.T) {
	w := hourly("c", now.Add(-85*time.Minute))
	run := "7aa2549c-0503-5f93-ab58-0ab78e7eb555"
	w.RunInFlight = &run
	d := BuildData([]postgres.CatalogEntry{entry("postgres://db/public/late", w)}, now)
	if d.Rows[0].Status != catalog.Late || d.Rows[0].Writers[0].RunInFlight == nil {
		t.Fatalf("row = %+v", d.Rows[0])
	}
}
