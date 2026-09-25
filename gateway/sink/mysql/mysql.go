// Package mysql writes a gateway's batches into a MySQL table.
//
// Importing it costs almost nothing -- database/sql and the driver, around
// half a megabyte -- which makes it the cheapest sink after Postgres.
package mysql

import (
	"context"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
	tomysql "github.com/AreteAcademy/brevis/sdk/to/mysql"
)

// Sink is what the YAML calls this driver.
const Sink = "mysql"

// New builds the driver from a config block.
//
// The modes are the same two words as everywhere else, and they mean the same
// two things. What differs is the machinery underneath:
//
//	append  a multi-row INSERT per block, inside a transaction. MySQL has no
//	        COPY, so this is an order of magnitude below Postgres on the same
//	        hardware -- flush wider here than there.
//	merge   INSERT IGNORE, which needs a UNIQUE index on ingestion_id. The
//	        driver refuses without one, for the reason Postgres does: without
//	        the index there is nothing to match and every redelivery lands
//	        again.
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
	t := tomysql.Table{DSN: dsn, Name: s.Table}
	if b.Target != nil {
		t.CreateTable = b.Target.Create
	}
	return &sink{
		table:  t,
		dedup:  gateway.DedupFor(s.Write),
		target: b.Target,
		name:   "mysql:" + s.Table + " (" + s.Write + ")",
	}, nil
}

type sink struct {
	table  tomysql.Table
	dedup  sdk.Dedup
	target *gateway.Target
	name   string
}

func (m *sink) Describe() string { return m.name }

func (m *sink) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	opt := sdk.WriteOptions{Dedup: m.dedup}
	if m.target != nil {
		opt.Schema = m.target.Schema
	}
	res, err := m.table.Write(ctx, batch, opt)
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}
