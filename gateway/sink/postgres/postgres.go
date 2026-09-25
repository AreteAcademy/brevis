// Package postgres writes a gateway's batches into a Postgres table.
//
// Importing it costs you pgx and nothing else: about 300 KB, which is why it
// is the one sink a slim build keeps.
package postgres

import (
	"context"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// Sink is what the YAML calls this driver.
const Sink = "postgres"

// New builds the driver from a config block.
//
// The two write modes are the SDK driver's two paths, not a layer of this
// package's own:
//
//	append  COPY FROM STDIN -- Postgres's fast path. No per-row round trip and
//	        no index lookup per row, which is why it is the one to reach for
//	        when the table is a log.
//	merge   the batch staged into a TEMP table and inserted onto the target
//	        with ON CONFLICT (ingestion_id) DO NOTHING, in ONE transaction:
//	        BEGIN, CREATE TEMP … ON COMMIT DROP, COPY, INSERT, COMMIT. A crash
//	        between any two of those leaves the table as it was, which is the
//	        ACID part of this and the reason the staging table exists.
//
// `merge` needs a UNIQUE index on ingestion_id and the driver refuses without
// one rather than silently appending -- which is the failure the mode exists to
// prevent. The refusal travels the normal way: the batch is retried, then
// buried in the dead letter with the reason.
//
// What makes merge mean anything is that ingestion_id is the SAME id a batch
// fetcher computes for the same record: the frozen UUID v5 over
// provider|entity|source_key|record_ts. So a row this gateway lands and a row a
// pipeline lands are one row, with no reconciliation between them.
func New(b gateway.Build) (gateway.Sinker, error) {
	s := b.Sink
	if err := gateway.CheckWrite(s); err != nil {
		return nil, err
	}
	if err := gateway.CheckTable(s); err != nil {
		return nil, err
	}
	dsn, err := gateway.DSNFrom(s)
	if err != nil {
		return nil, err
	}
	return &sink{
		table: topg.Table{DSN: dsn, Name: s.Table},
		dedup: gateway.DedupFor(s.Write),
		name:  "postgres:" + s.Table + " (" + s.Write + ")",
	}, nil
}

type sink struct {
	table topg.Table
	dedup sdk.Dedup
	name  string
}

func (p *sink) Describe() string { return p.name }

func (p *sink) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	// No Columns. A pipeline declares them because its rows have one shape it
	// controls; a gateway's batch is whatever N clients posted in the last
	// flush window, and the two shapes are allowed to differ.
	//
	// The driver resolves the column list from the table itself, intersected
	// with what the batch carries, BEFORE touching the server -- so a field the
	// table does not have is refused with the message that fixes it instead of
	// failing mid-COPY, and a field one event omits is written as NULL.
	res, err := p.table.Write(ctx, batch, sdk.WriteOptions{Dedup: p.dedup})
	if res == nil {
		return 0, err
	}
	// RowsLoaded on the error path too: reporting zero for a partial write has
	// the operator re-send what already landed.
	return res.RowsLoaded, err
}
