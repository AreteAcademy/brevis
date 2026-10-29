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
	// Judged against its own schedule -- on time or late depending on the
	// hour the test runs, so only "judged" is pinned, not which verdict.
	if r := steps["report"]; r.URI != "file:///data/reports/2026-10-15/" || r.Status == catalog.Unscheduled ||
		r.Status == catalog.Continuous || r.Rows == nil || *r.Rows != 4 {
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

// THE LOADS LIST: one group per workflow, its header saying the schedule in
// words (or that it runs by hand) and how many destinations, and under it one
// row per step and destination, `step → URI`, each going to its load.
func TestTheLoadsListGroupsByWorkflow(t *testing.T) {
	now := time.Now()
	page := draw(t, Loads(BuildLoads(printFixtures(now), now).Apply(DataFilter{})))
	for _, want := range []string{
		`data-filter-group="daily_sales"`, "every day at 04:00", "3 destinations",
		`data-filter-group="vendors_bacen"`, "manual",
		`href="/ingest/loads/daily_sales/report"`, "report", "file:///", "data/reports/2026-10-15/",
		"legacy", `data-filter-for="load-rows"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the loads list has no %s", want)
		}
	}
	for _, never := range []string{"events_gateway", "dead-letter", "pubsub://"} {
		if strings.Contains(page, never) {
			t.Errorf("a gateway stream is listed as a load: %s", never)
		}
	}
	if !strings.Contains(page, "6 destinations") || !strings.Contains(page, "3 workflows") {
		t.Error("the header does not count destinations and workflows")
	}
}

// THE FILTERS NARROW, COUNT AND STAY IN THE URL. A status or kind drops the
// rows outside it, and a group with none left; the search hides rather than
// drops, so deleting a letter brings a row back.
func TestTheLoadsListFilters(t *testing.T) {
	now := time.Now()
	v := BuildLoads(printFixtures(now), now)
	legacy := draw(t, Loads(v.Apply(DataFilter{Legacy: true, Path: "/ingest/loads"})))
	if strings.Contains(legacy, `data-filter-group="daily_sales"`) || !strings.Contains(legacy, `data-filter-group="vendors_bacen"`) {
		t.Error("legacy-only did not narrow to the legacy load's workflow")
	}
	searched := v.Apply(DataFilter{Q: "warehouse", Path: "/ingest/loads"})
	if searched.Shown() != 1 {
		t.Errorf("searching warehouse shows %d loads, want 1", searched.Shown())
	}
	page := draw(t, Loads(searched))
	if !strings.Contains(page, `href="/ingest/loads?kind=file"`) && !strings.Contains(page, `href="/ingest/loads?kind=file&amp;q=warehouse"`) {
		t.Error("a kind choice does not stay on /ingest/loads")
	}
	if got := (DataFilter{Path: "/ingest/loads"}).With("status", "late"); got != "/ingest/loads?status=late" {
		t.Errorf("a filter link = %q", got)
	}
}

// THE PREVIEW TAB STAYS ON THE LOAD'S OWN URL, with or without ?target=.
func TestALoadsTabsStayOnItsPage(t *testing.T) {
	v := TargetView{Self: "/ingest/loads/daily_sales/report"}
	if got := v.TabHref("preview"); got != "/ingest/loads/daily_sales/report?tab=preview" {
		t.Errorf("tab link = %q", got)
	}
	v.Self += "?target=s3%3A%2F%2Flake%2Fa%2F"
	if got := v.TabHref("preview"); !strings.HasSuffix(got, "&tab=preview") {
		t.Errorf("tab link with a target = %q", got)
	}
}

// THE STREAMS LIST: one section per gateway, one card per stream -- its path,
// format, hook and a compact flow with the dead letter hanging off the
// destination -- and no dead letter as a card of its own. Traffic is named
// where it lives, never drawn.
func TestTheStreamsListDrawsEachStreamWithItsDeadLetter(t *testing.T) {
	v := BuildStreams(printFixtures(time.Now())).Apply(StreamFilter{})
	page := draw(t, Streams(v))
	if n := strings.Count(page, "data-stream-card"); n != 3 {
		t.Fatalf("%d stream cards, want 3 (a dead letter became a card?)", n)
	}
	at := strings.Index(page, `data-stream-card="events_gateway /v1/clicks"`)
	end := strings.Index(page[at:], `data-stream-card="events_gateway /v1/orders"`)
	if at < 0 || end < 0 {
		t.Fatal("the clicks and orders cards are not in order")
	}
	clicks := page[at : at+end]
	for _, want := range []string{"POST", "/v1/clicks", "json", "enrich_clicks", "pubsub://brevis-local/clicks",
		"file:///var/dead-letter/clicks/", "refused batches", `href="/ingest/streams/events_gateway/v1/clicks#config"`} {
		if !strings.Contains(clicks, want) {
			t.Errorf("the clicks card has no %s", want)
		}
	}
	for _, want := range []string{`data-filter-group="events_gateway"`, "2 streams", "brevis_gateway_", "/metrics",
		"3 streams", "2 gateways", "2 with a dead letter", `data-filter-for="stream-cards"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the streams list has no %s", want)
		}
	}
	// The stream with no configuration still draws where it lands.
	if !strings.Contains(page, "bigquery://zarv-development-94b6/bronze/*") {
		t.Error("a stream with no configuration is not drawn")
	}
}

// THE FILTERS: a kind narrows to the streams whose sink is of it; the search
// looks in the path, the sink, the dead letter and the gateway, and hides.
func TestTheStreamsListFilters(t *testing.T) {
	v := BuildStreams(printFixtures(time.Now()))
	pubsub := draw(t, Streams(v.Apply(StreamFilter{Kind: "pubsub"})))
	if strings.Count(pubsub, "data-stream-card") != 1 || strings.Contains(pubsub, `data-filter-group="zarv_ingestion_dev"`) {
		t.Error("the pubsub filter did not narrow to the one pubsub stream")
	}
	if got := v.Apply(StreamFilter{Q: "dead-letter/orders"}).Shown(); got != 1 {
		t.Errorf("searching a dead letter shows %d streams, want 1", got)
	}
	if got := (StreamFilter{Q: "x"}).With("kind", "postgres"); got != "/ingest/streams?kind=postgres&q=x" {
		t.Errorf("a filter link = %q", got)
	}
}

// A STREAM'S PAGE: POST <path> as its name, and three panels -- Overview,
// Configuration (#config), Dead letter (#deadletter) -- all rendered, so with
// no script each is an anchor on one page and ui.js shows one at a time.
func TestAStreamPageHasItsThreeTabs(t *testing.T) {
	s := StreamsOf(printFixtures(time.Now()))[0].Streams[0] // clicks
	page := draw(t, StreamPage(BuildStream(s, nil)))
	for _, want := range []string{
		">POST /v1/clicks</h1>", "events_gateway",
		`data-hash-tabs`, `data-tab="overview"`, `data-tab="config"`, `data-tab="deadletter"`,
		`id="overview"`, `id="config"`, `id="deadletter"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the stream page has no %s", want)
		}
	}
	at := strings.Index(page, `id="deadletter"`)
	dl := page[at:]
	for _, want := range []string{"file:///var/dead-letter/clicks/", "data-copy", `brevis_gateway_dead_letter_records_total{stream=&#34;clicks&#34;}`} {
		if !strings.Contains(dl, want) {
			t.Errorf("the dead letter tab has no %s", want)
		}
	}
}

// THE CONFIGURATION TAB IS EVERYTHING THE STREAM DECLARES IN ONE PLACE, and a
// setting it does not declare is "—", never a guess. Merge is "—" when no
// write mode is declared, "yes" when it merges.
func TestAStreamsConfigurationSaysWhatIsUndeclared(t *testing.T) {
	gws := StreamsOf(printFixtures(time.Now()))
	clicks := draw(t, StreamPage(BuildStream(gws[0].Streams[0], nil)))
	cfg := clicks[strings.Index(clicks, `id="config"`):strings.Index(clicks, `id="deadletter"`)]
	for _, want := range []string{"enrich_clicks", "1s", "500", "pubsub://brevis-local/clicks", "file:///var/dead-letter/clicks/"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the configuration has no %s", want)
		}
	}
	if !strings.Contains(cfg, `data-declared="identity.provider">—`) {
		t.Error("an undeclared identity is not shown as —")
	}
	if !strings.Contains(cfg, `data-declared="merge">—`) {
		t.Error("an undeclared write mode is called merge or not")
	}
	orders := draw(t, StreamPage(BuildStream(gws[0].Streams[1], nil)))
	if !strings.Contains(orders, `data-declared="merge">yes`) {
		t.Error("a merging stream is not called merge")
	}
}

// QUERY DESTINATION ONLY WHERE SQL CAN READ IT: not Pub/Sub, not a file.
func TestAStreamQueriesOnlyATable(t *testing.T) {
	gws := StreamsOf(printFixtures(time.Now()))
	if BuildStream(gws[0].Streams[0], nil).Queryable() {
		t.Error("a Pub/Sub sink offers a query")
	}
	if !BuildStream(gws[0].Streams[1], nil).Queryable() {
		t.Error("a Postgres sink offers no query")
	}
}

// SEND TEST EVENT IS A DRAWER THAT SENDS NOTHING: a body in the stream's own
// format, validated as you type, and the curl that would send it -- with
// $GATEWAY and $GATEWAY_TOKEN where the console does not know the address or
// the token. No form, no action: the console posts nothing.
func TestTheTestEventDrawerSendsNothing(t *testing.T) {
	s := StreamsOf(printFixtures(time.Now()))[0].Streams[0] // clicks, json
	page := draw(t, StreamPage(BuildStream(s, nil)))
	if !strings.Contains(page, `href="#test-event"`) || !strings.Contains(page, `data-dialogo="test-event"`) {
		t.Error("there is no Send test event action")
	}
	at := strings.Index(page, `<dialog id="test-event"`)
	if at < 0 {
		t.Fatal("there is no drawer")
	}
	d := page[at : at+strings.Index(page[at:], "</dialog>")]
	for _, want := range []string{`data-test-body`, `data-test-format="json"`, `data-test-status`, `data-test-curl`,
		`$GATEWAY/v1/clicks`, `$GATEWAY_TOKEN`, "--data-binary @-", `{&#34;event&#34;: &#34;test&#34;}`} {
		if !strings.Contains(d, want) {
			t.Errorf("the drawer has no %s", want)
		}
	}
	if strings.Contains(d, `method="post"`) || strings.Contains(d, "action=") {
		t.Error("the drawer can post")
	}
}

// THE EXAMPLE IS IN THE STREAM'S FORMAT: a list for `array`, one object per
// line for `ndjson`, and the source key an identity declares filled in.
func TestTheTestEventExampleFollowsTheFormat(t *testing.T) {
	for _, c := range []struct{ format, key, want string }{
		{"array", "", `[{"event": "test"}]`},
		{"ndjson", "", "{\"event\": \"test\"}\n{\"event\": \"test\"}"},
		{"json", "table_name", `{"event": "test", "table_name": "test"}`},
	} {
		f := []catalog.ManifestField{{Path: "format", Value: c.format}}
		if c.key != "" {
			f = append(f, catalog.ManifestField{Path: "identity.source_key", Value: c.key})
		}
		v := StreamView{Stream: Stream{Path: "/v1/x", Config: &postgres.WriterConfig{Fields: f}}}
		if got := v.TestBody(); got != c.want {
			t.Errorf("%s example = %q, want %q", c.format, got, c.want)
		}
	}
}

// BESIDE THE CHART, WHAT RUNS NEXT (screen-overview-v2 OV3): the eight
// soonest triggers, each its workflow, cron and time -- where the donut was;
// the donut's four counts are in the strip.
func TestTheOverviewsChartSitsBesideTheNextTriggers(t *testing.T) {
	now := time.Date(2026, 10, 17, 3, 48, 0, 0, time.UTC)
	page := draw(t, Overview(OverviewData{Window: ParseWindow("7d"), Now: now,
		Proximas: []NextRun{{Workflow: "id_verification", Cron: "15 * * * *", When: now.Add(12 * time.Minute)}}}))
	if strings.Contains(page, ">Distribution<") {
		t.Error("the donut panel is still drawn")
	}
	if !strings.Contains(page, ">Runs per day<") {
		t.Error("a 7-day window is not charted by day")
	}
	at := strings.Index(page, ">Next triggers<")
	if at < 0 {
		t.Fatal("no next triggers panel")
	}
	next := page[at:]
	next = next[:strings.Index(next, "</section>")]
	// The time itself is the "in 12m"'s tooltip (screen-overview T5).
	for _, want := range []string{`href="/workflows/id_verification"`, "15 * * * *", "in 12m", `title="2026-10-17 04:00:00 UTC"`} {
		if !strings.Contains(next, want) {
			t.Errorf("the next triggers lost %s", want)
		}
	}
}

// THE RECENT RUNS ARE A TAB with the way to all of them, each run linked.
func TestTheOverviewsRecentRunsAreATab(t *testing.T) {
	now := time.Date(2026, 10, 17, 3, 48, 0, 0, time.UTC)
	recent := []postgres.RunSummary{{ID: "8d36ef6e-0000-0000-0000-000000000001", WorkflowSlug: "daily_sales", Status: "success", TriggerType: "schedule", CreatedAt: now.Add(-time.Hour)}}
	page := draw(t, Overview(OverviewData{Window: ParseWindow(""), Now: now, Recentes: recent}))
	at := strings.Index(page, `id="recent" data-tab-panel="recent"`)
	if at < 0 {
		t.Fatal("no recent runs tab")
	}
	panel := page[at:]
	panel = panel[:strings.Index(panel, "</section>")]
	for _, want := range []string{`href="/runs"`, `href="/runs/8d36ef6e-0000-0000-0000-000000000001"`, ">Workflow<"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the recent runs lost %s", want)
		}
	}
	if strings.Contains(page, ">In progress<") || strings.Contains(page, "data-overview-meta") {
		t.Error("the In progress panel or the status line is still drawn: the KPIs say what runs")
	}
}
