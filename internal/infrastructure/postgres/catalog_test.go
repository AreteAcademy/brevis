package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// landAt records one landing for a fresh run of `slug` whose slot was `slot`
// and whose data landed `lag` later.
func landAt(t *testing.T, pool *postgres.Pool, slug, node, target string, slot time.Time, lag time.Duration, rows *int64) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := aged(t, pool, slug, time.Hour, dom.StatusSuccess)
	if _, err := pool.Exec(ctx, `UPDATE runs SET logical_date = $2, criado_em = $2 WHERE id = $1`, id, slot); err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewRunRepo(pool).RecordLandings(ctx, id, dom.Step(node), slug,
		[]app.Landing{{Target: target, Rows: rows, At: slot.Add(lag)}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func schedule(t *testing.T, pool *postgres.Pool, slug, cron, tz string, active bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO schedules (id, workflow_slug, cron, timezone, ativo) VALUES ($1, $2, $3, $4, $5)`,
		uuid.New(), slug, cron, tz, active); err != nil {
		t.Fatal(err)
	}
}

func catalogDB(t *testing.T) *postgres.Pool {
	t.Helper()
	pool := landingsDB(t)
	if _, err := pool.Exec(context.Background(), `TRUNCATE runs, task_runs, schedules, gateway_destinations, gateway_streams, gateway_config_history, step_config_history, writer_configs CASCADE`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func entryFor(t *testing.T, entries []postgres.CatalogEntry, target string) postgres.CatalogEntry {
	t.Helper()
	for _, e := range entries {
		if e.Target == target {
			return e
		}
	}
	t.Fatalf("no entry for %q in %+v", target, entries)
	return postgres.CatalogEntry{}
}

func TestTheCatalogGroupsWritersUnderTheirTarget(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	const orders = "postgres://analytics/public/orders"
	base := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

	schedule(t, pool, "orders_sync", "0 * * * *", "UTC", true)
	for h := 0; h < 3; h++ {
		landAt(t, pool, "orders_sync", "load", orders, base.Add(time.Duration(h)*time.Hour), 5*time.Minute, n(int64(100+h)))
	}
	landAt(t, pool, "backfill_orders", "load", orders, base.Add(-48*time.Hour), time.Minute, n(9000))
	landAt(t, pool, "daily_report", "build", "bigquery://acme/silver/report", base, 2*time.Minute, nil)

	entries, err := postgres.NewReadRepo(pool).Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}

	e := entryFor(t, entries, orders)
	if e.Kind != "postgres" || e.Legacy {
		t.Errorf("kind=%q legacy=%v", e.Kind, e.Legacy)
	}
	if len(e.Writers) != 2 {
		t.Fatalf("writers = %+v, want orders_sync and backfill_orders", e.Writers)
	}
	var sync postgres.CatalogWriter
	for _, w := range e.Writers {
		if w.Workflow == "orders_sync" {
			sync = w
		}
	}
	if sync.Node != "load" || !sync.LastLoaded.Equal(base.Add(2*time.Hour+5*time.Minute)) {
		t.Errorf("orders_sync last = %+v", sync)
	}
	if sync.LastRows == nil || *sync.LastRows != 102 {
		t.Errorf("last rows = %v, want 102", sync.LastRows)
	}
	if !sync.HasSchedule || sync.Cron != "0 * * * *" || !sync.Active {
		t.Errorf("schedule = %+v", sync)
	}
	if sync.Lag != 5*time.Minute {
		t.Errorf("lag = %v, want the 5m every landing took", sync.Lag)
	}

	// The last loads of the TARGET, oldest first, across both writers.
	if len(e.Recent) != 4 || e.Recent[0] == nil || *e.Recent[0] != 9000 || *e.Recent[3] != 102 {
		t.Errorf("recent = %v", e.Recent)
	}

	r := entryFor(t, entries, "bigquery://acme/silver/report")
	if r.Writers[0].HasSchedule {
		t.Error("daily_report has no schedule")
	}
	if r.Writers[0].LastRows != nil {
		t.Error("absent rows must stay absent")
	}
}

// The catalog outlives retention, so a writer whose runs were purged still
// appears; with no slot left to measure, its lag is zero rather than an error.
func TestAWriterWhoseRunsWerePurgedStillAppears(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	id := landAt(t, pool, "old_job", "load", "mysql://shop/customers", time.Now().Add(-time.Hour), time.Minute, n(1))
	if _, err := pool.Exec(ctx, `DELETE FROM runs WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	entries, err := postgres.NewReadRepo(pool).Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := entryFor(t, entries, "mysql://shop/customers").Writers[0]
	if w.Workflow != "old_job" || w.Lag != 0 {
		t.Fatalf("writer = %+v", w)
	}
}

func TestALegacyLabelIsItsOwnEntry(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	id := aged(t, pool, "payments", time.Hour, dom.StatusSuccess)
	if _, err := pool.Exec(ctx, `
		INSERT INTO landings (run_id, node_id, map_index, target, workflow_slug, loaded_at, rows_written, legacy)
		VALUES ($1, 'load', -1, 'bronze.payments', 'payments', now(), 10, true)`, id); err != nil {
		t.Fatal(err)
	}
	landAt(t, pool, "payments", "load", "bigquery://acme/bronze/payments", time.Now(), 0, n(11))

	entries, err := postgres.NewReadRepo(pool).Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !entryFor(t, entries, "bronze.payments").Legacy {
		t.Error("the label is not marked legacy")
	}
	if entryFor(t, entries, "bigquery://acme/bronze/payments").Legacy {
		t.Error("the real target is marked legacy")
	}
}

// A writer whose workflow has a run queued or executing says so, and names it,
// so a late status can link to the run that is about to fix it.
func TestAWriterWithARunInFlightNamesIt(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	landAt(t, pool, "orders_sync", "load", "postgres://analytics/public/orders", time.Now().Add(-3*time.Hour), time.Minute, n(1))
	running := aged(t, pool, "orders_sync", time.Minute, dom.StatusRunning)

	entries, err := postgres.NewReadRepo(pool).Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := entryFor(t, entries, "postgres://analytics/public/orders").Writers[0]
	if w.RunInFlight == nil || *w.RunInFlight != running.String() {
		t.Fatalf("run in flight = %v, want %s", w.RunInFlight, running)
	}
}

func TestCatalogTargetReturnsItsWritersAndLastLoads(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	const orders = "postgres://analytics/public/order%20items"
	base := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	schedule(t, pool, "orders_sync", "0 * * * *", "UTC", true)
	var last uuid.UUID
	for h := 0; h < 3; h++ {
		last = landAt(t, pool, "orders_sync", "load", orders, base.Add(time.Duration(h)*time.Hour), 5*time.Minute, n(int64(10*(h+1))))
	}
	landAt(t, pool, "other", "load", "bigquery://a/b/c", base, 0, n(1))

	d, err := postgres.NewReadRepo(pool).CatalogTarget(ctx, orders)
	if err != nil {
		t.Fatal(err)
	}
	if d == nil || d.Target != orders || len(d.Writers) != 1 || d.Writers[0].Workflow != "orders_sync" {
		t.Fatalf("detail = %+v", d)
	}
	if len(d.Loads) != 3 {
		t.Fatalf("loads = %+v", d.Loads)
	}
	newest := d.Loads[0]
	if newest.RunID != last.String() || *newest.Rows != 30 || newest.Workflow != "orders_sync" {
		t.Errorf("newest load = %+v, want run %s with 30 rows", newest, last)
	}

	missing, err := postgres.NewReadRepo(pool).CatalogTarget(ctx, "postgres://nowhere/public/x")
	if err != nil || missing != nil {
		t.Fatalf("an unknown target = %+v, %v; want nil, nil", missing, err)
	}
}

func publish(t *testing.T, pool *postgres.Pool, streams ...catalog.ManifestStream) {
	t.Helper()
	m := catalog.Manifest{Kind: "gateway-manifest", Version: 1, Gateway: "edge", Streams: streams}
	if err := postgres.NewGatewayRepo(pool).Publish(context.Background(), m, time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC), "test"); err != nil {
		t.Fatal(err)
	}
}

// A gateway's published destinations join the catalog by target: one table
// written by a step and by a gateway is one entry with two writers.
func TestPublishedGatewayDestinationsJoinTheCatalog(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	const clicks = "bigquery://acme/landing/clicks"
	landAt(t, pool, "clicks_backfill", "load", clicks, time.Now().Add(-time.Hour), time.Minute, n(10))
	publish(t, pool, catalog.ManifestStream{Path: "/v1/clicks", Destinations: []catalog.ManifestDestination{
		{Role: "sink", Kind: "bigquery", Target: target(clicks)},
		{Role: "dead_letter", Kind: "files", Note: "relative path"},
		{Role: "archive", Kind: "files", Target: target("gs://acme-oversize/clicks/")},
	}})

	entries, err := postgres.NewReadRepo(pool).Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := entryFor(t, entries, clicks)
	var gw, step int
	for _, w := range e.Writers {
		if w.Gateway != nil {
			gw++
			if w.Gateway.Name != "edge" || w.Gateway.Stream != "/v1/clicks" || w.Gateway.Role != "sink" {
				t.Errorf("gateway writer = %+v", w.Gateway)
			}
		} else {
			step++
		}
	}
	if gw != 1 || step != 1 {
		t.Fatalf("writers: %d gateway, %d step; want one of each", gw, step)
	}

	archive := entryFor(t, entries, "gs://acme-oversize/clicks/")
	if archive.Kind != "gs" || len(archive.Writers) != 1 || archive.Writers[0].Gateway == nil {
		t.Errorf("a gateway-only destination = %+v", archive)
	}

	// The unnamed one is listed, with its note, never dropped.
	var unnamed *postgres.CatalogEntry
	for i := range entries {
		if entries[i].Target == "" {
			unnamed = &entries[i]
		}
	}
	if unnamed == nil || unnamed.Writers[0].Gateway.Note != "relative path" {
		t.Fatalf("the unnamed destination is missing or lost its note: %+v", unnamed)
	}

	d, err := postgres.NewReadRepo(pool).CatalogTarget(ctx, clicks)
	if err != nil || d == nil || len(d.Writers) != 2 {
		t.Fatalf("the destination page lost the gateway writer: %+v, %v", d, err)
	}
}

// The destination page reads each gateway writer's configuration -- the
// settings its stream runs with and the file's comments -- and only the page:
// the list does not need it and does not pay for it.
func TestTheDestinationPageCarriesTheGatewaysConfiguration(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	const tables = "bigquery://acme/landing/*"
	m := catalog.Manifest{Kind: "gateway-manifest", Version: 2, Gateway: "edge", Streams: []catalog.ManifestStream{{
		Name: "tables", Path: "/v1/tables",
		Fields: []catalog.ManifestField{
			{Path: "buffer.flush.every", Value: "1m"},
			{Path: "buffer.workers", Value: float64(4), Default: true},
			{Path: "sink.into.write", Value: "append"},
		},
		Notes: map[string]string{"sink.into.write": "APPEND: the landing table is history."},
		Destinations: []catalog.ManifestDestination{
			{Role: "sink", Kind: "auto_table", Target: target(tables), Routes: true},
		},
	}}}
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	if err := postgres.NewGatewayRepo(pool).Publish(ctx, m, at, "test"); err != nil {
		t.Fatal(err)
	}

	d, err := postgres.NewReadRepo(pool).CatalogTarget(ctx, tables)
	if err != nil || d == nil || len(d.Writers) != 1 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	c := d.Writers[0].Gateway.Config
	if c == nil {
		t.Fatal("the gateway writer has no configuration")
	}
	if len(c.Fields) != 3 || c.Fields[2].Path != "sink.into.write" || c.Fields[2].Value != "append" {
		t.Fatalf("fields = %+v", c.Fields)
	}
	if !c.Fields[1].Default || c.Fields[1].Value != float64(4) {
		t.Fatalf("the default was lost: %+v", c.Fields[1])
	}
	if c.Notes["sink.into.write"] != "APPEND: the landing table is history." {
		t.Fatalf("notes = %v", c.Notes)
	}

	entries, err := postgres.NewReadRepo(pool).Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if entryFor(t, entries, tables).Writers[0].Gateway.Config != nil {
		t.Fatal("the list read the configuration it never shows")
	}
}

// A stream published before version 2 has no configuration to show, and its
// page opens as it always did.
func TestAVersionOneGatewayHasNoConfigurationOnItsPage(t *testing.T) {
	pool := catalogDB(t)
	const clicks = "pubsub://acme/clicks"
	publish(t, pool, catalog.ManifestStream{Path: "/v1/clicks", Destinations: []catalog.ManifestDestination{
		{Role: "sink", Kind: "pubsub", Target: target(clicks)}}})

	d, err := postgres.NewReadRepo(pool).CatalogTarget(context.Background(), clicks)
	if err != nil || d == nil {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	if d.Writers[0].Gateway.Config != nil {
		t.Fatalf("config = %+v, want none", d.Writers[0].Gateway.Config)
	}
}

// The page reads a stream's configurations over time, newest first, one per
// change: a publish that changed nothing is not a version.
func TestTheDestinationPageReadsTheConfigurationsHistory(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	const clicks = "pubsub://acme/clicks"
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	stream := func(every string) catalog.Manifest {
		return catalog.Manifest{Kind: "gateway-manifest", Version: 2, Gateway: "edge", Streams: []catalog.ManifestStream{{
			Name: "clicks", Path: "/v1/clicks",
			Fields: []catalog.ManifestField{{Path: "buffer.flush.every", Value: every}},
			Destinations: []catalog.ManifestDestination{
				{Role: "sink", Kind: "pubsub", Target: target(clicks)}},
		}}}
	}
	repo := postgres.NewGatewayRepo(pool)
	for i, every := range []string{"1m", "1m", "2m"} {
		if err := repo.Publish(ctx, stream(every), at.Add(time.Duration(i)*time.Hour), "test"); err != nil {
			t.Fatal(err)
		}
	}

	d, err := postgres.NewReadRepo(pool).CatalogTarget(ctx, clicks)
	if err != nil || d == nil {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	h := d.Writers[0].Gateway.Config.History
	if len(h) != 2 {
		t.Fatalf("history = %+v, want two versions", h)
	}
	if h[0].Fields[0].Value != "2m" || !h[0].At.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("newest = %+v", h[0])
	}
	if h[1].Fields[0].Value != "1m" || !h[1].At.Equal(at) {
		t.Fatalf("oldest = %+v, want the FIRST publish of 1m", h[1])
	}
}

// landWith records one landing for a fresh run, written with cfg.
func landWith(t *testing.T, pool *postgres.Pool, slug, target string, at time.Time, cfg *app.WriterConfig) uuid.UUID {
	t.Helper()
	id := aged(t, pool, slug, time.Hour, dom.StatusSuccess)
	if err := postgres.NewRunRepo(pool).RecordLandings(context.Background(), id, dom.Step("load"), slug,
		[]app.Landing{{Target: target, Rows: n(1), At: at, Config: cfg}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func sdkConfig(dedup string) *app.WriterConfig {
	fields := []catalog.ManifestField{
		{Path: "source.driver", Value: "from.HTTP"},
		{Path: "target.dedup", Value: dedup},
	}
	hash, body := catalog.FieldsHash(fields)
	return &app.WriterConfig{Hash: hash, Body: body}
}

// A step's configuration on its destination's page: the one it last wrote
// with, and every change before it, each with the run where it first ran.
func TestTheDestinationPageCarriesAStepsConfigurationAndItsRuns(t *testing.T) {
	pool := catalogDB(t)
	ctx := context.Background()
	const orders = "bigquery://acme/silver/orders"
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	first := landWith(t, pool, "orders_sync", orders, at, sdkConfig("none"))
	landWith(t, pool, "orders_sync", orders, at.Add(time.Hour), sdkConfig("none"))
	changed := landWith(t, pool, "orders_sync", orders, at.Add(2*time.Hour), sdkConfig("merge"))
	landWith(t, pool, "orders_sync", orders, at.Add(3*time.Hour), sdkConfig("merge"))
	landWith(t, pool, "python_job", orders, at, nil)

	d, err := postgres.NewReadRepo(pool).CatalogTarget(ctx, orders)
	if err != nil || d == nil {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	var step, python *postgres.CatalogWriter
	for i := range d.Writers {
		switch d.Writers[i].Workflow {
		case "orders_sync":
			step = &d.Writers[i]
		case "python_job":
			python = &d.Writers[i]
		}
	}
	if step == nil || python == nil {
		t.Fatalf("writers = %+v", d.Writers)
	}
	if python.Config != nil {
		t.Fatalf("a step that never described itself has a configuration: %+v", python.Config)
	}
	c := step.Config
	if c == nil || c.Fields[1].Value != "merge" {
		t.Fatalf("config = %+v, want the latest", c)
	}
	if len(c.History) != 2 {
		t.Fatalf("history = %+v, want two versions", c.History)
	}
	if c.History[0].RunID != changed.String() || !c.History[0].At.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("newest = %+v, want the run where merge FIRST ran", c.History[0])
	}
	if c.History[1].RunID != first.String() {
		t.Fatalf("oldest = %+v, want the first run", c.History[1])
	}
}
