package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func gw(name, stream, role string, fields ...catalog.ManifestField) postgres.CatalogWriter {
	w := &postgres.GatewayWriter{Name: name, Stream: stream, Role: role}
	if len(fields) > 0 {
		w.Config = &postgres.WriterConfig{Fields: fields}
	}
	return postgres.CatalogWriter{Gateway: w}
}

func step(workflow, node, cron string, last time.Time, rows int64) postgres.CatalogWriter {
	return postgres.CatalogWriter{Workflow: workflow, Node: node, LastLoaded: last, LastRows: &rows,
		HasSchedule: cron != "", Cron: cron, Timezone: "UTC", Active: cron != ""}
}

// printFixtures are the catalog as the prints show it: events_gateway's two
// streams, each with a sink and a dead letter; a third stream with no dead
// letter; daily_sales's three steps; a legacy label; and a step that writes
// two destinations.
func printFixtures(now time.Time) []postgres.CatalogEntry {
	f := func(path string, v any) catalog.ManifestField { return catalog.ManifestField{Path: path, Value: v} }
	ago := now.Add(-47 * time.Minute)
	return []postgres.CatalogEntry{
		{Target: "pubsub://brevis-local/clicks", Kind: "pubsub", Writers: []postgres.CatalogWriter{
			gw("events_gateway", "/v1/clicks", "sink", f("name", "clicks"), f("path", "/v1/clicks"),
				f("format", "json"), f("hook", "enrich_clicks"), f("buffer.flush.every", "1s"),
				f("buffer.flush.records", 500), f("sink.type", "pubsub"))}},
		{Target: "file:///var/dead-letter/clicks/", Kind: "file", Writers: []postgres.CatalogWriter{
			gw("events_gateway", "/v1/clicks", "dead_letter")}},
		{Target: "postgres://warehouse/sales/events", Kind: "postgres", Writers: []postgres.CatalogWriter{
			gw("events_gateway", "/v1/orders", "sink", f("path", "/v1/orders"), f("format", "json"),
				f("sink.type", "postgres"), f("sink.write", "merge"))}},
		{Target: "file:///var/dead-letter/orders/", Kind: "file", Writers: []postgres.CatalogWriter{
			gw("events_gateway", "/v1/orders", "dead_letter")}},
		{Target: "bigquery://zarv-development-94b6/bronze/*", Kind: "bigquery", Writers: []postgres.CatalogWriter{
			gw("zarv_ingestion_dev", "/v1/ingestion", "sink")}},
		{Target: "file:///data/reports/2026-10-15/", Kind: "file", Writers: []postgres.CatalogWriter{
			step("daily_sales", "report", "0 4 * * *", ago, 4)}},
		{Target: "postgres://warehouse/sales/daily", Kind: "postgres", Writers: []postgres.CatalogWriter{
			step("daily_sales", "load_warehouse", "0 4 * * *", ago, 2)}},
		{Target: "file:///data/landing/", Kind: "file", Writers: []postgres.CatalogWriter{
			step("daily_sales", "load", "0 4 * * *", ago, 2)}},
		{Target: "bronze.vendors_bacen_sgs", Legacy: true, Writers: []postgres.CatalogWriter{
			step("vendors_bacen", "fetch_series", "", now.Add(-15*24*time.Hour), 0)}},
		{Target: "s3://lake/a/", Kind: "s3", Writers: []postgres.CatalogWriter{step("multi", "split", "", ago, 1)}},
		{Target: "s3://lake/b/", Kind: "s3", Writers: []postgres.CatalogWriter{step("multi", "split", "", ago, 1)}},
	}
}

// A STREAM IS THE UNIT: one sink and its dead letter, keyed by gateway and
// path. A dead letter joins its stream and is never an item of its own.
func TestIngestStreamsJoinEachDeadLetterToItsStream(t *testing.T) {
	gws := StreamsOf(printFixtures(time.Now()))
	if len(gws) != 2 || gws[0].Name != "events_gateway" || gws[1].Name != "zarv_ingestion_dev" {
		t.Fatalf("gateways = %+v", gws)
	}
	ev := gws[0].Streams
	if len(ev) != 2 {
		t.Fatalf("events_gateway has %d streams, want 2 (a dead letter became a stream?)", len(ev))
	}
	clicks := ev[0]
	if clicks.Path != "/v1/clicks" || clicks.Sink.URI != "pubsub://brevis-local/clicks" || clicks.Sink.Kind != "pubsub" {
		t.Errorf("clicks = %+v", clicks)
	}
	if clicks.DeadLetter == nil || clicks.DeadLetter.URI != "file:///var/dead-letter/clicks/" || clicks.DeadLetter.Kind != "file" {
		t.Errorf("clicks' dead letter = %+v", clicks.DeadLetter)
	}
	if clicks.Field("hook") != "enrich_clicks" || clicks.Field("format") != "json" {
		t.Error("the stream does not read its declared fields")
	}
	if ev[1].Path != "/v1/orders" || ev[1].DeadLetter == nil || ev[1].Merge() == nil || !*ev[1].Merge() {
		t.Errorf("orders = %+v (merge is sink.write == merge)", ev[1])
	}
	if clicks.Merge() != nil {
		t.Error("a stream that does not declare its write mode is called merge or not")
	}
	ing := gws[1].Streams
	if len(ing) != 1 || ing[0].DeadLetter != nil || ing[0].Config != nil {
		t.Errorf("a stream with no dead letter and no config = %+v", ing)
	}
	for _, g := range gws {
		for _, s := range g.Streams {
			if strings.Contains(s.Sink.URI, "dead-letter") {
				t.Errorf("a dead letter is a stream's sink: %+v", s)
			}
		}
	}
}

// A LOAD SITS UNDER THE WORKFLOW THAT WRITES IT, one target per step and
// destination, judged against that step's own schedule.
func TestIngestLoadsGroupByTheWorkflowThatWrites(t *testing.T) {
	now := time.Now()
	groups := LoadsOf(printFixtures(now), now)
	var names []string
	for _, g := range groups {
		names = append(names, g.Workflow)
	}
	if strings.Join(names, " ") != "daily_sales multi vendors_bacen" {
		t.Fatalf("groups = %v (a gateway stream became a load?)", names)
	}
	ds := groups[0]
	if ds.Cron != "0 4 * * *" || len(ds.Targets) != 3 {
		t.Fatalf("daily_sales = %+v", ds)
	}
	steps := map[string]LoadTarget{}
	for _, tg := range ds.Targets {
		steps[tg.Step] = tg
	}
	for _, want := range []string{"report", "load_warehouse", "load"} {
		if _, ok := steps[want]; !ok {
			t.Errorf("daily_sales has no %s", want)
		}
	}
	if r := steps["report"]; r.URI != "file:///data/reports/2026-10-15/" || r.Status != catalog.OnTime || r.Rows == nil || *r.Rows != 4 {
		t.Errorf("report = %+v", r)
	}
	if v := groups[2]; !v.Targets[0].Legacy || v.Cron != "" || v.Targets[0].Status != catalog.Unscheduled {
		t.Errorf("the legacy, manual load = %+v", v.Targets[0])
	}
}

// A LOAD'S LINK NAMES ITS DESTINATION only when its step writes more than one.
func TestIngestHrefs(t *testing.T) {
	now := time.Now()
	groups := LoadsOf(printFixtures(now), now)
	if h := groups[0].Targets[0].Href(); !strings.HasPrefix(h, "/ingest/loads/daily_sales/") || strings.Contains(h, "?") {
		t.Errorf("a one-destination step's link = %q", h)
	}
	multi := groups[1].Targets
	if len(multi) != 2 || !strings.Contains(multi[0].Href(), "?target=s3%3A%2F%2Flake%2Fa%2F") {
		t.Errorf("a two-destination step's link = %q", multi[0].Href())
	}
	s := StreamsOf(printFixtures(now))[0].Streams[0]
	if s.Href() != "/ingest/streams/events_gateway/v1/clicks" {
		t.Errorf("a stream's link = %q", s.Href())
	}
}
