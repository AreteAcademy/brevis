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

// THE REAL LAYER, and the one floci_test.go has named in a comment since it
// was written without anything behind the name.
//
// It exists because of #43. The whole fix rests on a premise the emulated
// layer cannot test: that BigQuery FOLDS COLUMN CASE on a load job. floci
// runs queries on DuckDB and refuses load jobs outright, by design, so the
// one thing that had to be true was the one thing nothing here could check
// -- and the premise arrived as a reporter's measurement, which is not the
// same as a measurement.
//
// It costs a real project and a disposable dataset:
//
//	export BREVIS_IT_PROJECT=my-project
//	export BREVIS_IT_DATASET=brevis_ci_sandbox   # must already exist
//
// These tests CREATE and DROP tables in that dataset and nothing else. Point
// them at a dataset you are willing to lose.
func realBigQuery(t *testing.T) (context.Context, *bigquery.Client, itEnv) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipped under -short")
	}
	project := os.Getenv("BREVIS_IT_PROJECT")
	if project == "" {
		t.Skip("BREVIS_IT_PROJECT not set; skipping the real-BigQuery layer")
	}
	dataset := os.Getenv("BREVIS_IT_DATASET")
	if dataset == "" {
		t.Skip("BREVIS_IT_DATASET not set; skipping the real-BigQuery layer")
	}

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return ctx, client, itEnv{project: project, dataset: dataset}
}

// realLoader builds a Loader writing to a throwaway table, dropped at the end.
func realLoader(t *testing.T, env itEnv, cfg *core.LoadConfig) (*Loader, *bigquery.Table) {
	t.Helper()

	cfg.ProjectID = env.project
	cfg.Dataset = env.dataset
	cfg.Table = fmt.Sprintf("bq43_%d", time.Now().UnixNano())
	if cfg.Format == "" {
		cfg.Format = "ndjson"
	}

	l, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("building the loader: %v", err)
	}
	table := l.bq.Dataset(cfg.Dataset).Table(cfg.Table)
	t.Cleanup(func() {
		if err := table.Delete(context.Background()); err != nil {
			t.Logf("could not drop %s: %v", cfg.Table, err)
		}
	})
	return l, table
}

// THE PREMISE ITSELF, measured rather than quoted. [#43]
//
// A load job writing `createdat` into a table whose column is `createdAt`
// lands in `createdAt`. Written with the BigQuery client directly and not
// through the SDK, because the SDK is what is being justified: if this ever
// stops being true, the fold in the loader becomes silent data loss and this
// test is what says so first.
func TestAgainstRealBigQueryALoadJobFoldsColumnCase(t *testing.T) {
	ctx, client, env := realBigQuery(t)

	name := fmt.Sprintf("bq43_premise_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(name)
	schema := bigquery.Schema{
		{Name: "id", Type: bigquery.StringFieldType},
		{Name: "createdAt", Type: bigquery.StringFieldType},
	}
	if err := table.Create(ctx, &bigquery.TableMetadata{Schema: schema}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	src := bigquery.NewReaderSource(strings.NewReader(
		`{"id":"1","createdat":"2026-10-08T00:00:00Z"}` + "\n"))
	src.SourceFormat = bigquery.JSON
	src.Schema = schema

	job, err := table.LoaderFrom(src).Run(ctx)
	if err != nil {
		t.Fatalf("starting the load: %v", err)
	}
	status, err := job.Wait(ctx)
	if err != nil {
		t.Fatalf("waiting on the load: %v", err)
	}
	if err := status.Err(); err != nil {
		t.Fatalf("BigQuery refused `createdat` into a `createdAt` column, "+
			"which is the premise #43's whole fix rests on: %v", err)
	}

	q := client.Query(fmt.Sprintf(
		"SELECT createdAt FROM `%s.%s.%s`", env.project, env.dataset, name))
	it, err := q.Read(ctx)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	var row struct{ CreatedAt string }
	if err := it.Next(&row); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if row.CreatedAt == "" {
		t.Error("the load succeeded and the column is empty: the value went " +
			"somewhere other than the column that folds to its name")
	}
}

// THE REPORTER'S FLUSH, end to end. [#43]
//
// Three events from one producer in one flush: two DELETEs carrying
// `nationalID`, an INSERT carrying `nationalId`. No record carries both. The
// schema derived from the union used to carry both, and BigQuery refused the
// CREATE with `Field nationalId already exists in schema`.
func TestAgainstRealBigQueryAFlushWithTwoSpellingsCreatesOneColumn(t *testing.T) {
	ctx, client, env := realBigQuery(t)

	cfg := &core.LoadConfig{
		CreateTable: true,
		Evolve:      core.EvolveAdditiveFromPayload,
		PartitionBy: "brevis_received_at",
		Schema: core.Schema{
			{Name: "brevis_ingestion_id", Type: core.TypeString, Required: true},
			{Name: "brevis_operation", Type: core.TypeString, Required: true},
			{Name: "brevis_received_at", Type: core.TypeTimestamp, Required: true},
		},
	}
	cfg.Columns = cfg.Schema.Names()
	l, table := realLoader(t, env, cfg)

	event := func(id, op, national string) core.Envelope {
		return core.Envelope{
			Provider: "p", Entity: "id_workspace_blacklist", SourceKey: id, RecordTS: "t",
			Payload: map[string]any{
				"brevis_ingestion_id": id,
				"brevis_operation":    op,
				"brevis_received_at":  "2026-10-08T10:00:00Z",
				national:              "doc-" + id,
			},
		}
	}
	if _, err := l.Load(ctx,
		event("1", "DELETE", "nationalID"),
		event("2", "DELETE", "nationalID"),
		event("3", "INSERT", "nationalId"),
	); err != nil {
		t.Fatalf("the flush was refused: %v", err)
	}

	fields := fieldsOfTable(t, table)
	var spellings []string
	for name := range fields {
		if strings.EqualFold(name, "nationalid") {
			spellings = append(spellings, name)
		}
	}
	if len(spellings) != 1 {
		t.Fatalf("the table has %v for one field; BigQuery refuses a CREATE "+
			"carrying two spellings of one column", spellings)
	}
	if spellings[0] != "nationalID" {
		t.Errorf("the column is %q, wanted the first in sorted order", spellings[0])
	}

	// AND NO ROW LOST ITS VALUE. The column folds; the data has to arrive.
	q := client.Query(fmt.Sprintf(
		"SELECT COUNT(*) AS n FROM `%s.%s.%s` WHERE nationalID IS NOT NULL",
		env.project, env.dataset, cfg.Table))
	it, err := q.Read(ctx)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	var row struct{ N int64 }
	if err := it.Next(&row); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if row.N != 3 {
		t.Errorf("%d of 3 rows carry the column; the INSERT's spelling was "+
			"dropped on the way to the wire", row.N)
	}
}

// THEIR SCRATCH CASE. [#43] The table exists with `createdAt`, the load
// declares `createdat`. Before the fold: "the Columns declaration lists
// createdat, which ... does not have", about a column that is there -- and
// under Evolve, an ADD COLUMN answered with 400 `Field createdat already
// exists in schema`.
func TestAgainstRealBigQueryADeclarationInAnotherCaseLoads(t *testing.T) {
	ctx, _, env := realBigQuery(t)

	cfg := &core.LoadConfig{
		Evolve: core.EvolveAdditive,
		Schema: core.Schema{
			{Name: "id", Type: core.TypeString},
			{Name: "createdat", Type: core.TypeTimestamp},
		},
	}
	cfg.Columns = cfg.Schema.Names()
	l, table := realLoader(t, env, cfg)

	// Created the way THEY created it, with the other spelling.
	if err := table.Create(ctx, &bigquery.TableMetadata{Schema: bigquery.Schema{
		{Name: "id", Type: bigquery.StringFieldType},
		{Name: "createdAt", Type: bigquery.TimestampFieldType},
	}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The check that runs BEFORE the extract, which is where the consumer
	// met this first and spent no quota finding out.
	if err := l.CheckDestination(ctx, cfg.Columns); err != nil {
		t.Fatalf("the pre-extract check refused a column the table has: %v", err)
	}

	if _, err := l.Load(ctx, core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t",
		Payload: map[string]any{"id": "A-1", "createdat": "2026-10-08T10:00:00Z"},
	}); err != nil {
		t.Fatalf("the load was refused: %v", err)
	}

	// AND NOTHING WAS ADDED. A second column differing only in case is the
	// 400 this whole issue is.
	fields := fieldsOfTable(t, table)
	if len(fields) != 2 {
		t.Errorf("the table now has %d columns: %v", len(fields), fields)
	}
	if _, added := fields["createdat"]; added {
		t.Error("a second column was added for the other spelling")
	}
}
