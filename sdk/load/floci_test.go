package load

import (
	"context"
	"fmt"
	"os"
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
