package check

import (
	"context"
	"fmt"
	"sort"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/project"
)

// Failure is a check that found rows.
type Failure struct {
	Check
	Rows int64
	// Value is one offending value, when the kind has one worth showing.
	// Empty for not_null, and empty when the sample query returned a NULL
	// -- a duplicated NULL cannot happen, so an empty one here means the
	// column held an empty string, which is a thing somebody needs to see
	// as "" rather than as nothing.
	Value string
}

// Result is the run.
type Result struct {
	Ran    int
	Failed []Failure
}

// Run runs every test of every model in `order`.
//
// IT DOES NOT STOP AT THE FIRST FAILURE, which is the opposite of Build and
// right for the opposite reason. A build that keeps going puts models on
// top of a relation that is not what their SQL assumed; a test run that
// stops hands somebody one problem at a time across four round trips to a
// warehouse. The whole report, once.
//
// A query the warehouse REFUSES is still an error and does stop it: that is
// not a model failing its test, it is this package having written SQL the
// warehouse will not take, and continuing would bury it under results.
func Run(ctx context.Context, d dialect.Dialect, conn dialect.Conn,
	p *project.Project, order []string) (Result, error) {

	var res Result
	for _, ref := range order {
		m, ok := p.Models[ref]
		if !ok {
			return res, fmt.Errorf("%s is in the order and not in the project", ref)
		}
		for _, t := range m.Tests {
			checks, err := For(d, m, t)
			if err != nil {
				return res, err
			}
			for _, c := range checks {
				res.Ran++

				n, err := conn.Scalar(ctx, c.Count)
				if err != nil {
					return res, fmt.Errorf("%s, test `%s` on %s:\n%s\n\n%w",
						c.Model, c.Kind, c.Column, c.Count, err)
				}
				rows := asInt(n)
				if rows == 0 {
					continue
				}

				f := Failure{Check: c, Rows: rows}
				if c.Sample != "" {
					v, err := conn.Scalar(ctx, c.Sample)
					if err != nil {
						return res, fmt.Errorf("%s, test `%s` on %s, reading an example:\n%s\n\n%w",
							c.Model, c.Kind, c.Column, c.Sample, err)
					}
					if v != nil {
						f.Value = fmt.Sprint(v)
					}
				}
				res.Failed = append(res.Failed, f)
			}
		}
	}

	// SORTED, so two runs over one project read the same. The order models
	// are built in is a dependency order and the order their failures are
	// READ in should not be -- somebody comparing today's report with
	// yesterday's is diffing text.
	sort.Slice(res.Failed, func(i, j int) bool {
		a, b := res.Failed[i], res.Failed[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Column < b.Column
	})
	return res, nil
}

// asInt reads a COUNT(*) from whichever Go type the driver chose.
//
// pgx hands back int64 and BigQuery's REST rows are strings, which is a
// DRIVER difference rather than a warehouse one -- the same reason
// dialect.KindFrom exists and lives beside the interface instead of in it.
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
