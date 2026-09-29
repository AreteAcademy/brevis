package load

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// The EMULATED layer: does the driver speak BigQuery's protocol?
//
// Named TestIntegration… deliberately, and the real-dataset tests are named
// TestAgainstRealBigQuery…. The two prove different things and the names say
// which, so that when somebody deletes the slow job to make the build faster,
// the name says what was deleted.
//
// WHAT THIS LAYER CANNOT PROVE, and it belongs here rather than in a wiki:
//
//   - QUOTA. The whole flush-floor design exists because BigQuery allows 1,500
//     load jobs per table per day. An emulator accepts 86,400 without
//     complaint, so a test that "proves" the window is safe proves nothing.
//   - CONCURRENCY ON DDL. ClaimWindow was tuned against a real failure: two
//     replicas met one new field and the loser spent its retries inside the
//     window. That needs the real service's locking and error timing.
//   - HOW JSON IS STORED. Shredded storage and JSON_VALUE pruning are why
//     ShapeDocument scales. An emulator keeping the column as text and
//     answering the same queries correctly hides exactly that property.
//
// Floci proves the driver speaks the protocol. It does not prove BigQuery
// behaves as we assumed.
func flociLoader(t *testing.T, cfg *core.LoadConfig) *Loader {
	t.Helper()
	if os.Getenv(EnvEmulator) == "" {
		t.Skipf("%s is not set; bring up the gcp profile:\n"+
			"  docker compose -f docker-compose.drivers.yml --profile gcp up -d\n"+
			"  export %s=http://localhost:4588",
			EnvEmulator, EnvEmulator)
	}
	// New refuses a config with no dataset or table, rightly. The real names
	// are minted per test below; these only get it past that check, and the
	// caller overwrites l.cfg with the real ones.
	if cfg.Dataset == "" {
		cfg.Dataset = "placeholder"
	}
	if cfg.Table == "" {
		cfg.Table = "placeholder"
	}
	l, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("building the loader: %v", err)
	}
	return l
}

// A dataset per test, because floci keeps everything in memory and a name
// reused across runs is a test that passes on the second run for the wrong
// reason.
// flociDataset mints a dataset and points the loader at it.
//
// It writes into l.cfg rather than replacing it, and that matters: New
// RESOLVES a config -- the GCS threshold, the format, the staging prefix all
// get their defaults there -- so handing the raw struct back afterwards throws
// every one of them away. It did, and a load that should have gone inline
// staged to a bucket that does not exist.
func flociDataset(t *testing.T, l *Loader, table string) {
	t.Helper()
	name := fmt.Sprintf("it_%d", time.Now().UnixNano())
	if err := l.bq.Dataset(name).Create(context.Background(), nil); err != nil {
		t.Fatalf("creating dataset %s: %v", name, err)
	}
	l.cfg.Dataset = name
	l.cfg.Table = table
}

// The declaration reaches the table: the columns, their types, the partition
// and the clustering.
//
// This is the shape of the v0.9.1 defect seen from the other side. There, the
// table was created correctly and every load was refused because the JOB
// described a different layout; here the assertion is that what the SDK
// declares is what the service records.
func TestIntegrationBigQueryCreatesWhatWasDeclared(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true,
		PartitionBy: "brevis_received_at",
		ClusterBy:   []string{"brevis_record_key"},
		Schema: core.Schema{
			{Name: "brevis_ingestion_id", Type: core.TypeString, Required: true},
			{Name: "brevis_record_key", Type: core.TypeString},
			{Name: "brevis_received_at", Type: core.TypeTimestamp, Required: true},
			{Name: "brevis_received_bytes", Type: core.TypeInt64},
			{Name: "data", Type: core.TypeJSON},
		},
	}
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "orders")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{Provider: "auto_table", Entity: "orders"}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	md, err := table.Metadata(ctx)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}

	if md.TimePartitioning == nil || md.TimePartitioning.Field != "brevis_received_at" {
		t.Errorf("partitioning = %+v; an unpartitioned landing table is a query "+
			"bill that only grows", md.TimePartitioning)
	}
	if md.TimePartitioning != nil && md.TimePartitioning.Type != bigquery.DayPartitioningType {
		t.Errorf("partition type = %v, want DAY", md.TimePartitioning.Type)
	}
	if md.Clustering == nil || len(md.Clustering.Fields) != 1 ||
		md.Clustering.Fields[0] != "brevis_record_key" {
		t.Errorf("clustering = %+v", md.Clustering)
	}

	want := map[string]bigquery.FieldType{
		"brevis_ingestion_id":   bigquery.StringFieldType,
		"brevis_record_key":     bigquery.StringFieldType,
		"brevis_received_at":    bigquery.TimestampFieldType,
		"brevis_received_bytes": bigquery.IntegerFieldType,
		"data":                  bigquery.JSONFieldType,
	}
	got := map[string]bigquery.FieldType{}
	for _, f := range md.Schema {
		got[f.Name] = f.Type
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("column %s is %v, declared %v", name, got[name], typ)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d columns, declared %d: %v", len(got), len(want), got)
	}
}

// A write does NOT reach BigQuery through this emulator, and the test says so
// out loud rather than leaving somebody to find out.
//
// The SDK writes only through load jobs -- inline, or staged through GCS above
// the threshold -- and floci implements neither. The refusal has two shapes
// depending on which door is knocked on, and both mean the same thing:
//
//	POST /bigquery/v2/projects/{p}/jobs
//	  400 {"message":"Only QUERY jobs are supported by the floci BigQuery emulator"}
//
//	POST /upload/bigquery/v2/projects/{p}/jobs?uploadType=multipart
//	  405, empty body -- no route at all
//
// The Go client takes the SECOND for an inline load, which is why this asserts
// only that the load failed. Pinning a message would make the test brittle
// about which door the client library chose, and that is not the property
// worth holding.
//
// So every assertion about a ROW belongs to the real-dataset job. This test
// exists to FAIL the day that stops being true: when floci gains load jobs,
// somebody reads this comment and moves the write assertions down a layer.
// Without it, "the emulator covers BigQuery" becomes folklore.
func TestIntegrationBigQueryStillRefusesLoadJobs(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true, Format: "ndjson",
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "brevis_ingestion_id", Type: core.TypeString, Required: true},
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
		},
		Columns: []string{"brevis_ingestion_id", "ts", "id"},
	}
	l := flociLoader(t, cfg)
	flociDataset(t, l, "rows")

	// The row carries every declared column, so CheckRow passes and the load
	// job is what fails. Without this the test would be measuring the
	// declaration check and reporting it as the emulator's limit.
	_, err := l.Load(context.Background(), core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t",
		Payload: map[string]any{
			"brevis_ingestion_id": "6b5a…",
			"ts":                  "2026-09-26T10:00:00Z",
			"id":                  "A-1",
		},
	})
	if err == nil {
		t.Fatal("the emulator accepted a load job. If floci implements them now, " +
			"this is the good news: move the row-level assertions off the " +
			"real-dataset job and onto this one, and delete this test")
	}
	// Logged rather than asserted: the SHAPE of the refusal belongs to floci
	// and to whichever route the client library picked, and neither is ours
	// to pin. What is ours is that the write did not happen.
	t.Logf("the emulator refused the load, as expected: %v", err)
}

// Additive evolution on BigQuery: the declaration grows a column and the
// table follows.
//
// This test was written the other way round a day ago, pinning the GAP -- and
// the gap was not theoretical. A consumer upgrading 0.10.0 to 0.11.0 sent
// 20,250 events into tables an older gateway had created and landed zero rows,
// because the eighth fixed column was in the declaration and not in the table:
//
//	JSON parsing error in row starting at position 0:
//	No such field: brevis_received_bytes
//
// `auto_table` had been declaring EvolveAdditive on every destination while
// this one ignored the field. Issue #34.
func TestIntegrationBigQueryEvolvesAdditively(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true,
		Evolve:      core.EvolveAdditive,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
		},
	}
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "grows")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// The same table, now declared with one more column -- exactly what
	// auto_table hands the driver when a record grows a field, and exactly
	// what 0.11.0 did to every table in the wild.
	l.cfg.Schema = core.Schema{
		{Name: "ts", Type: core.TypeTimestamp, Required: true},
		{Name: "id", Type: core.TypeString},
		{Name: "brevis_received_bytes", Type: core.TypeInt64},
		// REQUIRED in the declaration, and it must land NULLABLE: BigQuery
		// refuses a required column added to a table that already has rows,
		// and the rows already there have no value for it.
		{Name: "cupom", Type: core.TypeString, Required: true},
	}
	if err := l.evolveTable(ctx, table); err != nil {
		t.Fatalf("evolving: %v", err)
	}

	md, err := table.Metadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*bigquery.FieldSchema{}
	for _, f := range md.Schema {
		got[f.Name] = f
	}
	for _, name := range []string{"ts", "id", "brevis_received_bytes", "cupom"} {
		if got[name] == nil {
			t.Fatalf("%s is missing after the evolution; the load would fail at "+
				"the row with `No such field: %s`", name, name)
		}
	}
	if got["cupom"].Required {
		t.Error("cupom was added REQUIRED; BigQuery refuses that on a table with " +
			"rows, and the rows already there have no value for it")
	}
	if got["ts"] == nil || !got["ts"].Required {
		t.Error("the evolution relaxed a column that was already REQUIRED")
	}

	// Idempotent: a second pass finds nothing to do, which is what every
	// batch after the first one does.
	if err := l.evolveTable(ctx, table); err != nil {
		t.Errorf("a second evolution of an already-evolved table failed: %v", err)
	}
	after, err := table.Metadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Schema) != len(md.Schema) {
		t.Errorf("the second pass changed the table: %d columns, was %d",
			len(after.Schema), len(md.Schema))
	}
}

// And it stays OFF unless asked. EvolveNone is the zero value, so a caller who
// never heard of this keeps the old behaviour: the table is left alone and the
// declaration is checked against it.
func TestIntegrationBigQueryDoesNotEvolveUnlessAsked(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
		},
	}
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "frozen")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}
	l.cfg.Schema = append(l.cfg.Schema, core.Column{Name: "novo", Type: core.TypeString})
	if err := l.evolveTable(ctx, table); err != nil {
		t.Fatal(err)
	}
	md, err := table.Metadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range md.Schema {
		if f.Name == "novo" {
			t.Error("a load with EvolveNone altered the table")
		}
	}
}

// The wire: Load itself evolves, not just evolveTable called by hand.
//
// The test above exercises the function and says nothing about whether
// anything calls it -- cutting the call out of Load leaves it green, which is
// the defect this project keeps finding. This one goes through Load.
//
// The load job still fails, because floci implements none, and that is fine:
// the evolution happens BEFORE the job is submitted, so the column has to be
// there whatever the job then does. Asserting through a failure is the honest
// shape here rather than a reason to skip the test.
func TestIntegrationBigQueryLoadEvolvesBeforeItWrites(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true, Format: "ndjson",
		Evolve:      core.EvolveAdditive,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
		},
	}
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "wired")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	l.cfg.Schema = append(l.cfg.Schema,
		core.Column{Name: "brevis_received_bytes", Type: core.TypeInt64})

	// Fails on the load job. What matters is what happened before it.
	_, _ = l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t",
		Payload: map[string]any{
			"ts": "2026-09-26T10:00:00Z", "id": "A-1", "brevis_received_bytes": 100,
		},
	})

	md, err := table.Metadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range md.Schema {
		if f.Name == "brevis_received_bytes" {
			return
		}
	}
	t.Error("Load did not evolve the table before submitting the job, so the " +
		"column is still missing and every row would fail with `No such field`")
}

// The early check lets through what EvolveAdditive was asked to add — #41.
//
// BigQuery reaches this by a longer road than the SQL drivers: `Table.
// CheckDestination` builds a Loader and delegates here. The defect and the
// fix are the same — the check ran before the extract and refused the one
// difference the load had been told to repair, so the flag was unreachable
// in its only case.
//
// The check inside `Load` is untouched and must stay that way: it runs AFTER
// evolveTable, against fresh metadata, so it is the verification that
// evolving actually did what it said. Relaxing that one would lose the only
// thing watching evolve.
func TestIntegrationBigQueryTheEarlyCheckDefersToEvolve(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
		},
	}
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "earlycheck")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// A declaration with one column the table does not have.
	declared := []string{"ts", "id", "cupom"}

	// With nothing asked for, the early refusal stands. This is the property
	// the fix had to keep: one metadata query against a whole source quota
	// spent to learn a column does not match.
	l.cfg.Evolve = core.EvolveNone
	err := l.CheckDestination(ctx, declared)
	if err == nil {
		t.Fatal("the early check accepted a column the table lacks with no " +
			"evolution asked for: it stopped checking")
	}
	if !strings.Contains(err.Error(), "cupom") {
		t.Errorf("the refusal does not name the column: %v", err)
	}

	// Told to add it, the early check defers and lets the load get there.
	l.cfg.Evolve = core.EvolveAdditive
	if err := l.CheckDestination(ctx, declared); err != nil {
		t.Fatalf("the early check refused a column EvolveAdditive was asked "+
			"to add, so the flag never reaches evolveTable: %v", err)
	}
}

// fieldsOfTable is the table's schema, by name.
func fieldsOfTable(t *testing.T, table *bigquery.Table) map[string]*bigquery.FieldSchema {
	t.Helper()
	md, err := table.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*bigquery.FieldSchema{}
	for _, f := range md.Schema {
		out[f.Name] = f
	}
	return out
}

// EvolveAdditiveFromPayload on BigQuery: the batch teaches the table.
//
// This destination reaches evolve by the longest road — `evolveTable` is
// gated on the mode, and before the predicate landed that gate read
// `!= EvolveAdditive`, which would have returned early for this mode and
// shipped the whole feature inert here while Postgres and MySQL worked.
func TestIntegrationBigQueryTheBatchTeachesTheTable(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true, Format: "ndjson",
		Evolve:      core.EvolveAdditiveFromPayload,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
		},
	}
	cfg.Columns = cfg.Schema.Names()
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "taught")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// A field nobody declared, and an object beside it. The load job itself
	// may fail on the emulator; what matters is what happened before it.
	_, _ = l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t",
		Payload: map[string]any{
			"ts": "2026-09-26T10:00:00Z", "id": "A-1",
			"series": "21129",
			"meta":   map[string]any{"uf": "SP"},
		},
	})

	fields := fieldsOfTable(t, table)
	series, ok := fields["series"]
	if !ok {
		t.Fatal("the table has no `series` column: the batch did not reach " +
			"evolveTable, and every row would fail with `No such field`")
	}
	if series.Type != bigquery.StringFieldType {
		t.Errorf("series is %v, want STRING -- a scalar is text", series.Type)
	}
	if meta, ok := fields["meta"]; !ok {
		t.Error("the table has no `meta` column")
	} else if meta.Type != bigquery.JSONFieldType {
		t.Errorf("meta is %v, want JSON -- an object is JSON", meta.Type)
	}

	// NULLABLE, whatever the declaration says. BigQuery refuses a REQUIRED
	// column added to a table that already has rows, and it is right to: the
	// rows already there have no value for it.
	if series.Required {
		t.Error("the discovered column is REQUIRED: the rows already in the " +
			"table have no value for it")
	}
}

// The Loader's config is the LOADER's, and the batch must not keep it.
//
// This is the destination where extending in place is the natural thing to
// write: the SQL drivers receive WriteOptions by VALUE, so their extension is
// per-batch by construction, and here `l.cfg` is a pointer that outlives
// every batch. Extend it in place and the first batch's field becomes
// permanent — so the SECOND batch, which need not carry it, is refused for
// "a declared column the row does not have". The feature would break the
// batch after the one it helped.
func TestIntegrationBigQueryTheBatchDoesNotKeepTheConfig(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true, Format: "ndjson",
		Evolve:      core.EvolveAdditiveFromPayload,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
		},
	}
	cfg.Columns = cfg.Schema.Names()
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "notkept")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	before := len(l.cfg.Columns)

	_, _ = l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t",
		Payload: map[string]any{
			"ts": "2026-09-26T10:00:00Z", "id": "A-1", "efemera": "x",
		},
	})

	if len(l.cfg.Columns) != before {
		t.Errorf("the Loader's declaration grew from %d to %d columns: the "+
			"extension outlived the batch, and the next one is about to be "+
			"refused for a column it never carried", before, len(l.cfg.Columns))
	}
	if l.cfg.Schema.Has("efemera") {
		t.Error("the Loader's Schema kept `efemera`")
	}

	// And the proof that it matters: a second batch WITHOUT the field.
	_, err := l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k2", RecordTS: "t2",
		Payload: map[string]any{"ts": "2026-09-26T11:00:00Z", "id": "A-2"},
	})
	if err != nil && strings.Contains(err.Error(), "efemera") {
		t.Errorf("the batch after the one that taught the table was refused "+
			"for its column: %v", err)
	}
}

// EvolveAdditive is unchanged: it adds what the DECLARATION has, never what
// the batch carries.
func TestIntegrationBigQueryTheBatchTeachesNothingWithoutTheMode(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true, Format: "ndjson",
		Evolve:      core.EvolveAdditive,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
		},
	}
	cfg.Columns = cfg.Schema.Names()
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "unasked")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	_, err := l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t",
		Payload: map[string]any{
			"ts": "2026-09-26T10:00:00Z", "id": "A-1", "nao_declarada": "x",
		},
	})
	if err == nil {
		t.Fatal("EvolveAdditive accepted a field nothing declared")
	}
	if !strings.Contains(err.Error(), "nao_declarada") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
	if _, ok := fieldsOfTable(t, table)["nao_declarada"]; ok {
		t.Error("the table grew a column under EvolveAdditive")
	}
}

// The mode completes the declaration; it does not switch the check off.
//
// The same mutation survived here that survived on Postgres: replacing the
// extension with `if !FromPayload() { CheckRow }` passes everything else,
// because once the batch's fields are declared the undeclared half has
// nothing left to catch. The half it silently loses is a column the CONSUMER
// declared and the chain does not produce — which this mode has nothing to
// do with, and which is still a bug.
func TestIntegrationBigQueryTheModeCompletesTheDeclaration(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true, Format: "ndjson",
		Evolve:      core.EvolveAdditiveFromPayload,
		PartitionBy: "ts",
		Schema: core.Schema{
			{Name: "ts", Type: core.TypeTimestamp, Required: true},
			{Name: "id", Type: core.TypeString},
			// Declared by the consumer and never produced by the chain.
			{Name: "prometida", Type: core.TypeString},
		},
	}
	cfg.Columns = cfg.Schema.Names()
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "promised")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	_, err := l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t",
		Payload: map[string]any{
			"ts": "2026-09-26T10:00:00Z", "id": "A-1", "achada": "x",
		},
	})
	if err == nil {
		t.Fatal("a declared column the chain does not produce was accepted: " +
			"the row check was skipped instead of extended, and the half this " +
			"mode has nothing to do with went with it")
	}
	if !strings.Contains(err.Error(), "prometida") {
		t.Errorf("the refusal does not name the declared column: %v", err)
	}
	if strings.Contains(err.Error(), "which Columns does not declare") {
		t.Errorf("the batch's own column was refused as undeclared, which is "+
			"the thing this mode exists to stop: %v", err)
	}
}

// A landing pipeline declares the same columns a gateway would, on BigQuery.
//
// The third encoding: this destination does not convert per column, it
// marshals the whole row into NDJSON. So the marker has to survive
// encoding/json, which is what JSONText.MarshalJSON is for — without it the
// line carries "{\"uf\":\"SP\"}" and the JSON column receives a string.
func TestIntegrationBigQueryALandingPipelineDeclaresWhatAGatewayWould(t *testing.T) {
	// The landing layout's control columns, written out: this package cannot
	// reach sdk.LandingControlColumns without an import that exists only for
	// a test, and the point here is the two PRODUCER columns anyway.
	control := core.Schema{
		{Name: "brevis_ingestion_id", Type: core.TypeString, Required: true},
		{Name: "brevis_record_key", Type: core.TypeString},
		{Name: "brevis_operation", Type: core.TypeString, Required: true},
		{Name: "brevis_received_at", Type: core.TypeTimestamp, Required: true},
	}
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true, Format: "ndjson",
		Evolve:      core.EvolveAdditiveFromPayload,
		PartitionBy: "brevis_received_at",
		Schema:      control,
	}
	cfg.Columns = cfg.Schema.Names()
	l := flociLoader(t, cfg)
	ctx := context.Background()
	flociDataset(t, l, "landed")

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	if err := l.createFromSchema(ctx, table, provenance{}); err != nil {
		t.Fatalf("creating: %v", err)
	}

	row := map[string]any{
		"brevis_ingestion_id": "id-1",
		"brevis_operation":    "INSERT",
		"brevis_received_at":  "2026-09-29T10:00:00Z",
		"brevis_record_key":   "k-1",
		"valor":               "8.89",
		"meta":                core.JSONText(`{"uf":"SP"}`),
		"tags":                core.JSONText(`["a","b"]`),
	}
	_, _ = l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t", Payload: row,
	})

	fields := fieldsOfTable(t, table)
	for _, c := range []struct {
		name string
		want bigquery.FieldType
	}{
		{"meta", bigquery.JSONFieldType},
		{"tags", bigquery.JSONFieldType},
		{"valor", bigquery.StringFieldType},
	} {
		f, ok := fields[c.name]
		if !ok {
			t.Errorf("the table has no %q column", c.name)
			continue
		}
		if f.Type != c.want {
			t.Errorf("%s is %v, want %v -- a gateway declares it %v for the "+
				"same record", c.name, f.Type, c.want, c.want)
		}
	}
}
