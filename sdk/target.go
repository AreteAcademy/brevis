package sdk

import (
	"fmt"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Target says where records land, and what every destination honours.
//
// The destination itself is To -- bigquery.Table, postgres.Table, to.Files.
// What lives here instead of in the driver is what is true of all of them: the
// columns declared, the metadata asked for, the deduplication wanted.
//
// See ExampleTarget.
type Target struct {
	// To is the destination. Required.
	To Writer

	// Columns declares the destination's columns, in the order of its DDL,
	// including the ones the SDK fills in:
	//
	//	Columns: []string{
	//		"ingestion_id",         // from sdk.IngestionID()
	//		"ingestion_loaded_at",  // from sdk.IngestionLoadedAt()
	//		"provider",
	//		"entity",
	//		"source_key",
	//		"payload",
	//	}
	//
	// One declaration, and it names every column -- including the two that
	// the ingestion transformers write, so nothing lands that the chain did
	// not compose.
	//
	// Checked against the row the Transform chain composed, so a declared
	// column the chain did not deliver is an error naming the column, and a
	// field the row carries that this list does not declare is an error naming
	// the field. Checked again against the real destination,
	// where a declared column it lacks is an error naming both sides.
	//
	// Nil declares nothing and checks nothing.
	//
	// Use Schema instead when the destination has to CREATE the table: names
	// alone cannot say what type each column is, and the SDK does not guess.
	// Setting both is an error.
	Columns []string

	// Schema is Columns with a type on each column, and it is what a
	// destination needs in order to create the table.
	//
	//	Schema: sdk.Schema{
	//		{Name: "ingestion_id",        Type: sdk.TypeString,    Required: true},
	//		{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
	//		{Name: "temperatura",         Type: sdk.TypeFloat64},
	//	}
	//
	// Everything Columns does, Schema does -- it is the same declaration
	// carrying more. Setting both is an error: two lists of columns are two
	// sources of truth, and the one that loses does so silently.
	Schema core.Schema

	// PartitionBy names the column a created table is partitioned on.
	//
	// Empty keeps the SDK's default, which is daily on ingestion_loaded_at --
	// the column that says when the row was written, and the one a landing
	// table is almost always read by. Declaring it is how you say otherwise.
	//
	// Only consulted when the destination creates the table.
	PartitionBy string

	// Dedup selects deduplication. Zero value appends, which is free. What
	// DedupMerge costs, and whether a destination supports it at all, is the
	// driver's to say.
	Dedup core.Dedup

	// FlushEvery writes every N records read, instead of accumulating the whole
	// read in memory. Zero accumulates everything, which remains the default.
	//
	// A read over thousands of sources does not necessarily fit in memory: the
	// whole batch stays alive, and the destination builds a SECOND copy of it to
	// serialise. With FlushEvery the ceiling is N records plus the copy of N.
	//
	// What that costs, and it has to be said:
	//
	//   - the load stops being ATOMIC. A failure on the third batch leaves the
	//     first two written, and the re-run depends on Dedup not to duplicate.
	//     Without DedupMerge, a failure midway duplicates what already went in.
	//   - with DedupMerge each batch pays its own MERGE, so a small N multiplies
	//     the cost at the destination.
	//
	// Result sums the batches: Rows, Ignored and Bytes are the total, and
	// RowErrors gathers them all.
	FlushEvery int
}

// ValidateTarget checks what the facade can check without touching the
// destination: the declaration against itself.
//
// Exported because a fetcher may want to fail early, in a test or a -dry-run,
// with no cloud client at all -- and because an invariant that can only be
// exercised with a server running is an invariant nobody exercises.
func ValidateTarget(t Target) error { return t.validate() }

// validate checks what the facade owns. What the destination needs is the
// destination's to check, and it does so in Write.
func (d Target) validate() error {
	if d.To == nil {
		return fmt.Errorf("Target.To is required: pass a destination, such as " +
			"bigquery.Table{Dataset: \"bronze\", Name: \"pedidos\"}")
	}
	if len(d.Columns) > 0 && len(d.Schema) > 0 {
		return fmt.Errorf("Target declares both Columns and Schema, and they are two " +
			"lists of the same thing -- the one that loses would do so silently. " +
			"Schema is Columns with a type on each column: keep it and drop Columns")
	}
	if err := d.Schema.Check(); err != nil {
		return err
	}
	if d.PartitionBy != "" && len(d.Schema) > 0 {
		if !d.Schema.Has(d.PartitionBy) {
			return fmt.Errorf("Target.PartitionBy names %q, which Schema does not declare. "+
				"A table cannot be partitioned on a column it does not have", d.PartitionBy)
		}
	}
	return nil
}

// colunas devolve a declaracao efetiva, venha de Columns ou de Schema.
func (d Target) colunas() []string {
	if len(d.Schema) > 0 {
		return d.Schema.Names()
	}
	return d.Columns
}

// options folds the Target into what every driver receives.
func (d Target) options(run RunContext) core.WriteOptions {
	return core.WriteOptions{
		Columns:     d.colunas(),
		Schema:      d.Schema,
		PartitionBy: d.PartitionBy,
		Dedup:       d.Dedup,
		Run:         run,
	}
}

// Result describes what actually happened, end to end. Printing it is meant
// to be the whole of a fetcher's observability:
//
//	log.Info("pronto", res.Args()...)
type Result struct {
	// Extract
	Records      int64 // records that came out of the source, after expansion
	Pages        int   // pages fetched
	Attempts     int   // HTTP attempts spent, retries included
	ExtractBytes int64 // bytes read off the wire, before Transform
	ExtractTime  time.Duration

	// Load
	Rows         int64      // rows written
	Ignored      int64      // rows deduplication matched as already present
	Bytes        int64      // bytes in the staged format
	Strategy     string     // how the driver wrote: "inline", "gcs", "copy"
	Format       string     // the format actually written
	Dedup        core.Dedup // the deduplication that actually ran
	TableCreated bool       // whether this run created the destination
	Table        string     // the destination written to
	LoadTime     time.Duration

	// CredentialExpiry is when the source credential stops working, when the
	// source renews one that says so. Zero otherwise.
	//
	// It is on Result and not only in a log line because the credential this
	// tracks is renewed by a human: whoever runs this pipeline is the one who
	// has to act, and a warning that only exists in the logs is how the
	// silent death happens in the first place.
	CredentialExpiry time.Time

	// CredentialStoreError says the rotated credential was not stored. The load
	// happened; what was lost is the rotation, and the next run falls back to
	// the seed. Empty when there is no store or when it did store.
	CredentialStoreError string

	// FailedSources are the sources that failed and were tolerated by from.Many
	// with ContinueOnError. Empty when there were none.
	//
	// It is here, and not only in the log, because it is the only thing that
	// allows reprocessing what is missing. A fan-out that loses 3,000 of 4,803
	// sources and does not say which forces the next run to redo everything.
	FailedSources []core.SourceFailure

	// Objects are the objects the load wrote and that are still there.
	//
	// With to.Files, the file. With a destination that stages and deletes,
	// empty -- a reported path that no longer exists is worse than none.
	//
	// It exists for the case where one step writes the file and another reads
	// it: without this, whoever wrote does not know what they wrote, because the
	// name carries a timestamp the driver chooses.
	Objects []string

	// CheckpointReused says this run read the extract from the depot instead of
	// from the source -- that is, that the vendor's quota was spared.
	//
	// It is here, and not only in the log, because a saving that shows up
	// nowhere is indistinguishable from not having saved anything.
	CheckpointReused bool

	// CheckpointPath is where this run's depot is. Empty when the checkpoint is
	// off.
	CheckpointPath string

	// CheckpointError says why the depot could not be written. The load
	// happened; what was lost is the insurance, and the next attempt will have
	// to redo the extract. Empty when it wrote or when it is off.
	CheckpointError string

	// Stages is what each stage did: how many records went in, how many came
	// out, and how many groups an aggregation produced.
	//
	// With four stages, "5,515 rows" says nothing about where the other six
	// million went. Without this, finding out means bisecting by hand.
	Stages []StageResult

	// Diagnostics the destination reported per row, when it refused any.
	RowErrors []string

	Duration time.Duration
}

// Args renders the result as slog key-value pairs.
func (r *Result) Args() []any {
	args := []any{
		"records", r.Records,
		"lines", r.Rows,
		"ignored", r.Ignored,
		"pages", r.Pages,
		"attempts", r.Attempts,
		"table", r.Table,
		"strategy", r.Strategy,
		"dedup", r.Dedup,
		"table_created", r.TableCreated,
		"extract", r.ExtractTime,
		"load", r.LoadTime,
		"duration", r.Duration,
	}

	// Counters not every driver fills are left out when they are zero.
	//
	// "a number that is always zero is worse than no number" is a principle of
	// this project, and a SQL pipeline's log line broke it: the database drivers
	// do not count bytes, so `extract_bytes=0 bytes=0 format=""` showed on every
	// run, teaching whoever read to skip those fields -- and when an HTTP
	// pipeline showed a real zero, nobody would see it.
	if r.ExtractBytes > 0 {
		args = append(args, "extract_bytes", r.ExtractBytes)
	}
	if r.Bytes > 0 {
		args = append(args, "bytes", r.Bytes)
	}
	if r.Format != "" {
		args = append(args, "format", r.Format)
	}
	// Only when there is one: a key that is always the zero time on every
	// line teaches people to skip it, and then it is invisible on the one
	// line that matters.
	if !r.CredentialExpiry.IsZero() {
		args = append(args,
			"credential_expires", r.CredentialExpiry.Format(time.RFC3339),
			"credential_left", core.RoundDuration(time.Until(r.CredentialExpiry)))
	}
	if r.CredentialStoreError != "" {
		args = append(args, "credential_not_saved", r.CredentialStoreError)
	}
	if n := len(r.FailedSources); n > 0 {
		args = append(args, "failed_sources", n)
	}
	// Only when there is something to say: `checkpoint=false` on every line
	// would teach people to skip the field, and the line that matters is
	// precisely the rare one.
	if r.CheckpointReused {
		args = append(args, "checkpoint", "reused", "checkpoint_at", r.CheckpointPath)
	}
	if r.CheckpointError != "" {
		args = append(args, "checkpoint_failed", r.CheckpointError)
	}
	if len(r.Objects) == 1 {
		args = append(args, "object", r.Objects[0])
	} else if len(r.Objects) > 1 {
		// With FlushEvery there are several, and dumping fifty paths into one
		// log line makes it unreadable. The whole list is in Result.Objects.
		args = append(args, "objects", len(r.Objects), "first", r.Objects[0])
	}
	return args
}

func (r *Result) String() string {
	return fmt.Sprintf("%d records -> %d lines (%d ignored) em %s via %s, dedup %s, %s",
		r.Records, r.Rows, r.Ignored, r.Table, r.Strategy, r.Dedup, r.Duration)
}
