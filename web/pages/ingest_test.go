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

// A LOAD'S LINK NAMES ITS DESTINATION only when its step writes more than one.
func TestIngestHrefs(t *testing.T) {
	now := time.Now()
	loads := map[string]LoadTarget{}
	for _, l := range LoadsOf(printFixtures(now), now) {
		loads[l.Step+" "+l.URI] = l
	}
	if h := loads["report file:///data/reports/2026-10-15/"].Href(); h != "/ingest/loads/daily_sales/report" {
		t.Errorf("a one-destination step's link = %q", h)
	}
	if h := loads["split s3://lake/a/"].Href(); !strings.Contains(h, "?target=s3%3A%2F%2Flake%2Fa%2F") {
		t.Errorf("a two-destination step's link = %q", h)
	}
	s := StreamsOf(printFixtures(now))[0].Streams[0]
	if s.Href() != "/ingest/streams/events_gateway/v1/clicks" {
		t.Errorf("a stream's link = %q", s.Href())
	}
}

// A FILTER LINK STAYS ON ITS PAGE, its fields always in one order.
func TestAFilterLinkStaysOnItsPage(t *testing.T) {
	if got := (DataFilter{Path: "/ingest/loads", Q: "x"}).With("status", "late"); got != "/ingest/loads?status=late&q=x" {
		t.Errorf("a filter link = %q", got)
	}
	if got := (DataFilter{Path: "/ingest/loads", Status: "late"}).With("source", "none"); got != "/ingest/loads?status=late&source=none" {
		t.Errorf("an origin link = %q", got)
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

// THE CONFIGURATION TAB IS EVERYTHING THE STREAM DECLARES, and only that
// (screen-streams T4): a part it does not declare has no card -- not a card
// of guesses -- and a write mode is the one it declares.
func TestAStreamsConfigurationSaysOnlyWhatIsDeclared(t *testing.T) {
	gws := StreamsOf(printFixtures(time.Now()))
	clicks := draw(t, StreamPage(BuildStream(gws[0].Streams[0], nil)))
	cfg := clicks[strings.Index(clicks, `id="config"`):strings.Index(clicks, `id="deadletter"`)]
	for _, want := range []string{"enrich_clicks", "1s", "500", `data-explained="sink.type"`, "pubsub"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the configuration has no %s", want)
		}
	}
	if strings.Contains(cfg, "Event identity") || strings.Contains(cfg, `data-explained="sink.write"`) {
		t.Error("an undeclared identity or write mode has a row")
	}
	orders := draw(t, StreamPage(BuildStream(gws[0].Streams[1], nil)))
	if !strings.Contains(orders, `data-explained="sink.write"`) || !strings.Contains(orders, ">merge<") {
		t.Error("a merging stream does not say so")
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
