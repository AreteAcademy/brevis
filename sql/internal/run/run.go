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
	// The schema is made ONCE, before the first model that lands in it, and
	// not once per model: EnsureSchema is safe to repeat -- the conformance
	// suite insists on it -- but repeating it would put N statements in the
	// log for one fact.
	made := map[string]bool{}

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

		res.Built = append(res.Built, Built{
			Ref: ref, Kind: m.Materialised, Was: was, Took: time.Since(started),
		})
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
