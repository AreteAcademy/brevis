// Package run builds a project's models against a warehouse.
//
// The loop is the same for every dialect and lives here exactly once: ask
// what is there, ask the dialect what to run, run it. Everything that
// differs per warehouse is behind the Dialect interface, and everything that
// differs per project was decided by `project` before this is called.
package run

import (
	"context"
	"fmt"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
	"github.com/AreteAcademy/brevis/sql/internal/project"
)

// Built is one model, after.
type Built struct {
	Ref  string
	Kind model.Materialisation
	// Was is what the relation had been. It is reported because "this was a
	// view until today" is the first thing somebody wants to know when a
	// query that worked yesterday is slow.
	Was  dialect.Kind
	Took time.Duration

	// Target is the catalog identity -- what goes on the `landed` line and
	// what puts this model on `/data`.
	Target string

	// Rows is how many the table holds, and it is NIL FOR A VIEW.
	//
	// A pointer because ABSENT IS NOT ZERO, which is the engine's own rule
	// for this field: "a step that does not count says nothing, and a nil
	// summed into a zero would draw a table that emptied overnight". A view
	// holds no rows at all, and reporting 0 for one is indistinguishable
	// from a table that really is empty.
	Rows *int64
}

// Result is the build.
type Result struct{ Built []Built }

// Build runs every model in `order`, which is already a dependency order.
//
// ORDER IS NOT COMPUTED HERE, and that is on purpose: `project.Select`
// produces it, `brevis-sql graph` prints the same thing, and a second
// ordering in this file is a second answer to one question.
//
// It stops at the first failure. A warehouse half built is a worse place to
// stand than one that stopped where the error is -- and continuing would
// build models on top of a relation that is not what their SQL assumed.
func Build(ctx context.Context, d dialect.Dialect, conn dialect.Conn,
	p *project.Project, order []string) (Result, error) {

	var res Result
	// FUNCTIONS FIRST, ALL OF THEM, before any model.
	//
	// Ordering them against the models would mean reading the SQL for
	// function CALLS, and the extractor deliberately does not -- a call in
	// a SELECT list is not a relation, and teaching it otherwise would give
	// every model that uses one a dependency on something that is not a
	// model. They are cheap and `create or replace` is idempotent in both
	// dialects, so all of them go first and the question does not arise.
	//
	// They are NOT in Built: a function holds no rows and nothing queries
	// it, so it is not a destination and emits no `landed` line.
	made := map[string]bool{}
	for _, f := range p.Functions {
		if !made[f.Schema] {
			for _, s := range d.EnsureSchema(f.Schema) {
				if err := conn.Exec(ctx, s); err != nil {
					return res, fmt.Errorf("making the schema for %s: %w", f.Path, err)
				}
			}
			made[f.Schema] = true
		}
		if err := conn.Exec(ctx, f.SQL); err != nil {
			return res, fmt.Errorf("%s: the warehouse refused this function:\n%s\n\n%w",
				f.Path, f.SQL, err)
		}
	}

	// The schema is made ONCE, before the first model that lands in it, and
	// not once per model: EnsureSchema is safe to repeat -- the conformance
	// suite insists on it -- but repeating it would put N statements in the
	// log for one fact.
	for _, ref := range order {
		m, ok := p.Models[ref]
		if !ok {
			return res, fmt.Errorf("%s is in the build order and not in the project", ref)
		}

		if !made[m.Schema] {
			for _, s := range d.EnsureSchema(m.Schema) {
				if err := conn.Exec(ctx, s); err != nil {
					return res, fmt.Errorf("making the schema for %s: %w", ref, err)
				}
			}
			made[m.Schema] = true
		}

		started := time.Now()
		was, err := kindOf(ctx, d, conn, ref)
		if err != nil {
			return res, fmt.Errorf("%s: asking what is there: %w", ref, err)
		}

		stmts, err := d.Build(m, was)
		if err != nil {
			return res, err
		}
		for _, s := range stmts {
			if err := conn.Exec(ctx, s); err != nil {
				// NAMED BY MODEL, and the statement with it. The server's own
				// message is "syntax error at or near", which on its own
				// sends somebody to read every file in the project.
				return res, fmt.Errorf("%s: the warehouse refused this:\n%s\n\n%w", ref, s, err)
			}
		}

		b := Built{
			Ref: ref, Kind: m.Materialised, Was: was,
			Target: conn.Target(ref),
		}
		// COUNTED ONLY FOR A TABLE, and with its own query. A table built
		// by CREATE TABLE AS holds rows somebody will want on `/data`;
		// asking the same of a view would report the row count of whatever
		// it selects from today, which is a number about the SOURCE and
		// changes without this model being rebuilt.
		if m.Materialised == model.Table {
			n, err := conn.Scalar(ctx, "SELECT COUNT(*) FROM "+ref)
			if err != nil {
				return res, fmt.Errorf("%s: counting what it wrote: %w", ref, err)
			}
			rows := asInt(n)
			b.Rows = &rows
		}
		b.Took = time.Since(started)
		res.Built = append(res.Built, b)
	}
	return res, nil
}

func kindOf(ctx context.Context, d dialect.Dialect, conn dialect.Conn, ref string) (dialect.Kind, error) {
	v, err := conn.Scalar(ctx, d.KindOf(ref))
	if err != nil {
		return dialect.Absent, err
	}
	return dialect.KindFrom(v), nil
}

// asInt reads a COUNT(*) from whichever Go type the driver chose.
//
// pgx hands back int64 and BigQuery's REST rows are strings, which is a
// DRIVER difference rather than a warehouse one -- the same reason
// dialect.KindFrom exists beside the interface instead of in it.
func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		var out int64
		_, _ = fmt.Sscan(n, &out)
		return out
	case []byte:
		var out int64
		_, _ = fmt.Sscan(string(n), &out)
		return out
	}
	return 0
}
