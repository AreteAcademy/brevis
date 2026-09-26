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

// Additive evolution is NOT implemented for BigQuery, and this pins it.
//
// `auto_table` declares Target.Evolve = EvolveAdditive on every destination.
// The gateway's postgres and mysql sinks read it; the bigquery sink never
// does, and nothing in sdk/load patches a table's schema -- the one
// table.Update there sets a description and labels.
//
// So "the table grows a column on its own" is true of Postgres and MySQL and
// FALSE of BigQuery, which is the destination the design is aimed at. The docs
// said otherwise and have been corrected.
//
// This test asserts the gap so it cannot be forgotten, and it FAILS the day
// somebody implements evolution -- which is when the docs go back.
func TestIntegrationBigQueryDoesNotEvolveASchemaYet(t *testing.T) {
	cfg := &core.LoadConfig{
		ProjectID: "floci-local", CreateTable: true,
		// Named, because partitionOf defaults to the SDK's own timestamp
		// column and creating a table partitioned on a column the schema does
		// not declare is refused -- by the emulator and by BigQuery alike.
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
	// auto_table hands the driver when a record grows a field.
	l.cfg.Schema = core.Schema{
		{Name: "ts", Type: core.TypeTimestamp, Required: true},
		{Name: "id", Type: core.TypeString},
		{Name: "cupom", Type: core.TypeString},
	}
	existed, err := l.prepareTable(ctx, table, nil, provenance{})
	if err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if !existed {
		t.Fatal("the table was reported absent right after being created")
	}

	md, err := table.Metadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range md.Schema {
		if f.Name == "cupom" {
			t.Fatal("BigQuery grew a column, so additive evolution now works " +
				"there. Delete this test, put the claim back in the docs, and " +
				"move the gateway's bigquery sink onto Target.Evolve")
		}
	}
	// The emulator PATCHes fine -- proven by hand against its REST API -- so
	// this is the SDK not asking, and not the emulator refusing.
	if _, err := table.Update(ctx, bigquery.TableMetadataToUpdate{
		Schema: append(md.Schema, &bigquery.FieldSchema{Name: "cupom", Type: bigquery.StringFieldType}),
	}, md.ETag); err != nil {
		t.Errorf("the emulator refused a schema PATCH, so this test cannot tell "+
			"'the SDK does not ask' from 'the emulator cannot': %v", err)
	}
}
