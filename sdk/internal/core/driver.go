package core

import (
	"context"
	"io"
	"iter"
)

// Reader produces records. One implementation per origin: from.HTTP,
// from.Files, from.Many, postgres.Query, mysql.Query.
//
// The driver is the value, not an enum: from.HTTP carries a URL and a Reading,
// postgres.Query carries a DSN and a query. Neither has to make room for the
// other's fields, which is what keeps a source struct from collecting forty
// options of which any one driver reads six.
//
// It also decides what a consumer compiles. Go prunes dependencies by package
// imported, never by field used -- so a fetcher that never imports the
// BigQuery destination never builds it.
type Reader interface {
	// Read opens the source and yields its records, lazily. The sequence must
	// stay lazy: a driver that materialises the whole source before returning
	// puts a 5 GB export in memory.
	Read(ctx context.Context, opt ReadOptions) (iter.Seq2[Envelope, error], error)

	// Describe names the origin for logs and errors, with any secret
	// redacted. "http://api.example.com/v1/events", "postgres://host/db#pedidos".
	Describe() string
}

// ReadOptions is what every source honours, whatever it reads from.
type ReadOptions struct {
	// Preview prints the first N records once the read finishes. See the
	// Preview fields on Source.
	Preview       int
	PreviewBytes  int
	PreviewWriter io.Writer

	// Stats, when not nil, is filled in as the read proceeds.
	Stats *Stats

	// Run is what the engine knows about this execution. A source that reads
	// incrementally takes its window from here.
	Run RunContext
}

// Writer consumes records. One implementation per destination.
//
// It receives records with provenance already resolved -- provider, entity,
// It receives records exactly as the Transform chain composed them --
// ingestion_id included, when the fetcher asked for it -- so no driver reads
// the caller's record to work out what identifies it.
type Writer interface {
	// Write sends the batch and reports what actually happened.
	Write(ctx context.Context, records []Envelope, opt WriteOptions) (*LoadResult, error)

	// Describe names the destination for logs and errors: "bronze.pedidos".
	Describe() string
}

// WriteOptions is what every destination honours, whatever it writes to.
type WriteOptions struct {
	// Columns declares the destination's columns, in DDL order, including the
	// two the ingestion transformers write. Nil declares nothing. See
	// sdk.Target.Columns.
	//
	// When Schema is filled in, these are its names: a driver that only checks
	// names does not need to know which of the two the consumer wrote.
	Columns []string

	// Schema is the declaration WITH types, and it is what a destination needs
	// in order to CREATE the table. Empty means the consumer declared only the
	// names, or nothing -- and in that case a destination that would create the
	// table has to refuse rather than infer.
	Schema Schema

	// Discovered names the columns the BATCH contributed rather than the
	// consumer, under EvolveAdditiveFromPayload.
	//
	// It exists so the row check can tell the two apart. "You declared X and
	// your chain does not produce it" is a bug worth stopping for; "the batch
	// carried X in record 50 and not in record 0" is what a landing table IS,
	// and the record missing it writes NULL there.
	//
	// Always a subset of Columns, and set only by WithDiscovered.
	Discovered []string

	// PartitionBy names the partitioning column of a created table.
	// Empty lets the destination use its own default.
	PartitionBy string

	// FoldsCase says the destination treats two spellings of one name as one
	// column. [#43]
	//
	// BigQuery does: a load job writing `createdat` into a table whose column
	// is `createdAt` lands in `createdAt`, and a CREATE listing both refuses
	// with "Field nationalId already exists in schema". Postgres and MySQL do
	// not -- a quoted identifier is distinct there, and two spellings are two
	// columns on purpose.
	//
	// It is a FACT ABOUT THE DESTINATION and it travels with the write, not a
	// mode the consumer chooses: Discovered is shared by the BigQuery loader
	// and the SQL drivers, and the fold has to reach one without reaching the
	// others. Only the destination sets it.
	//
	// It does NOT normalise anything. `BREVIS_NORMALIZE_DATA` lower-cases
	// every column and flattens nested objects, which is a one-way door on a
	// live table; this only decides whether two names are one column.
	FoldsCase bool

	// Dedup selects deduplication. What it costs, and whether it is supported
	// at all, is the driver's to say -- a directory of files has no key to
	// match on, and saying so is better than ignoring the option.
	Dedup Dedup

	// DedupKey is the column DedupMerge matches on. Empty means MetadataID --
	// `ingestion_id` -- which is what every pipeline writes and what this has
	// always been.
	//
	// It exists because a caller may own a table's whole shape and name its
	// identity column something else. The gateway's `auto_table` does: it
	// prefixes the columns it invents, so its identity is
	// `brevis_ingestion_id` -- and the drivers looked for `ingestion_id` by
	// NAME, found nothing, and every merge into a table they had just created
	// refused. The table was created and zero rows landed.
	//
	// It is a COLUMN NAME that reaches SQL, so it is validated rather than
	// quoted and hoped: see DedupKeyOf.
	DedupKey string

	// Run is what the engine knows about this execution. A destination that
	// creates its table on the first run reads that from here.
	Run RunContext
}
