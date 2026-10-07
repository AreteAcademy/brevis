package pages

import (
	"net/url"
	"strings"
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

func filterFixture() DataView {
	paused := hourly("p", now.Add(-72*time.Hour))
	paused.Active = false
	legacy := entry("bronze.payments", hourly("pay", now.Add(-10*time.Minute)))
	legacy.Kind, legacy.Legacy = "", true
	s3 := entry("s3://landing/vendors/", hourly("v", now.Add(-5*time.Hour)))
	s3.Kind = "s3"
	return BuildData([]postgres.CatalogEntry{
		entry("postgres://db/public/fresh", hourly("a", now.Add(-20*time.Minute))),
		entry("postgres://db/public/late", hourly("c", now.Add(-85*time.Minute))),
		entry("postgres://db/public/paused", paused),
		s3, legacy,
	}, now)
}

func targets(rows []DataRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Target)
	}
	return out
}

func TestFiltersNarrowTheRowsAndLeaveTheSummaryWhole(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"s3://landing/vendors/", "postgres://db/public/late", "bronze.payments", "postgres://db/public/fresh", "postgres://db/public/paused"}},
		{"status=attention", []string{"s3://landing/vendors/", "postgres://db/public/late"}},
		{"status=late", []string{"postgres://db/public/late"}},
		{"kind=s3", []string{"s3://landing/vendors/"}},
		{"legacy=1", []string{"bronze.payments"}},
		{"status=attention&kind=postgres", []string{"postgres://db/public/late"}},
		// What the page does not know is ignored, not an empty page.
		{"status=bogus&kind=snowflake&legacy=yes", []string{"s3://landing/vendors/", "postgres://db/public/late", "bronze.payments", "postgres://db/public/fresh", "postgres://db/public/paused"}},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		v := filterFixture().Apply(ParseDataFilter(q))
		if got := targets(v.Rows); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("?%s = %v, want %v", c.query, got, c.want)
		}
		if v.Total != 5 {
			t.Errorf("?%s: total = %d; the summary counts every destination", c.query, v.Total)
		}
	}
}

// Links are stable and shareable: the same filter always writes the same URL,
// and setting one field keeps the others.
func TestFilterLinksAreStableAndKeepTheOtherFields(t *testing.T) {
	f := DataFilter{Status: "attention", Kind: "s3"}
	if got := f.With("kind", "postgres"); got != "/data?status=attention&kind=postgres" {
		t.Errorf("With(kind) = %q", got)
	}
	if got := f.With("status", ""); got != "/data?kind=s3" {
		t.Errorf("clearing status = %q", got)
	}
	if got := (DataFilter{}).With("legacy", "1"); got != "/data?legacy=1" {
		t.Errorf("legacy = %q", got)
	}
	if got := (DataFilter{}).With("status", ""); got != "/data" {
		t.Errorf("no filter = %q", got)
	}
}

func TestTheFilterBarOffersOnlyKindsThatExist(t *testing.T) {
	v := filterFixture()
	if strings.Join(v.Kinds, ",") != "postgres,s3" || !v.HasLegacy {
		t.Errorf("kinds = %v, legacy = %v", v.Kinds, v.HasLegacy)
	}
}

func gatewayWriter(stream, role string) postgres.CatalogWriter {
	return postgres.CatalogWriter{Gateway: &postgres.GatewayWriter{
		Name: "edge", Stream: stream, Role: role, Kind: "pubsub",
		PublishedAt: time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)}}
}

// A gateway writes as events arrive: continuous, never late, and healthier
// than a late step -- so a table both feed is continuous when the step lags.
func TestGatewayWritersAreContinuousAndAnUnnamedOneUnidentified(t *testing.T) {
	both := entry("bigquery://acme/landing/clicks",
		hourly("backfill", now.Add(-85*time.Minute)), gatewayWriter("/v1/clicks", "sink"))
	only := entry("pubsub://acme/clicks", gatewayWriter("/v1/clicks", "sink"))
	only.Kind = "pubsub"
	unnamed := postgres.CatalogEntry{Writers: []postgres.CatalogWriter{gatewayWriter("/v1/clicks", "dead_letter")}}

	d := BuildData([]postgres.CatalogEntry{both, only, unnamed}, now)
	got := map[string]catalog.Status{}
	for _, r := range d.Rows {
		got[r.Target] = r.Status
	}
	if got["bigquery://acme/landing/clicks"] != catalog.Continuous ||
		got["pubsub://acme/clicks"] != catalog.Continuous || got[""] != catalog.Unidentified {
		t.Fatalf("statuses = %v", got)
	}
	// A gateway measures nothing on this page: the last load is the step's.
	for _, r := range d.Rows {
		if r.Target == "bigquery://acme/landing/clicks" && (r.LastWhen == nil || !r.LastWhen.Equal(now.Add(-85*time.Minute))) {
			t.Errorf("last loaded = %v, want the step's", r.LastWhen)
		}
		if r.Target == "pubsub://acme/clicks" && r.LastWhen != nil {
			t.Errorf("a gateway-only destination claims a last load: %v", r.LastWhen)
		}
	}
	// Unidentified sorts last, after everything that has a verdict.
	if d.Rows[len(d.Rows)-1].Target != "" {
		t.Errorf("the unnamed destination is not last: %v", targets(d.Rows))
	}
}
