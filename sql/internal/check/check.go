// Package check turns a model's `tests:` into SQL.
//
// A TEST IS A SELECT THAT RETURNS VIOLATING ROWS. Zero rows is a pass, and
// there is nothing else to interpret: no exit code to read, no count to
// compare against a threshold somebody has to remember, and no second
// language to learn. It is dbt's shape and it is the right one -- the query
// that reports the failure is a query somebody can paste into a console and
// keep narrowing.
//
// NOTHING HERE IS ON THE DIALECT INTERFACE except Literal, which is already
// there because it measurably differs. The four tests are standard SQL and
// both warehouses run the same text; adding a method per test kind would be
// an interface describing this package rather than describing a warehouse.
package check

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
)

// Check is one assertion, ready to run.
type Check struct {
	Model  string
	Kind   string
	Column string

	// Count is the query whose single value is how many rows break it.
	Count string

	// Sample is the query whose single value is ONE offending value, and it
	// is empty when the kind has none worth showing: every row that breaks
	// `not_null` breaks it by being NULL, and printing NULL back teaches
	// nobody anything.
	Sample string
}

// identifier is a column or relation name this package will concatenate
// into SQL.
//
// CONSERVATIVE FOR BOTH WAREHOUSES rather than asked of the dialect,
// because this one does not measurably differ -- and a name outside it is
// refused rather than quoted. There is no placeholder on this path: Conn
// takes a statement and nothing else, deliberately, so this rule is the
// only thing between a model's header and the warehouse.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// For is every check one `tests:` entry makes.
//
// A SLICE, because `not_null: [a, b, c]` is three checks and a failure has
// to name THE column. One check over three columns would report "not_null
// failed on orders", which is the report that sends somebody to read the
// model.
func For(d dialect.Dialect, m model.Model, t model.Test) ([]Check, error) {
	ref := m.Ref()
	name := func(what, s string) error {
		if identifier.MatchString(s) {
			return nil
		}
		return fmt.Errorf("%s, test `%s`: %s %q is not a name this will put into "+
			"SQL; it has to match %s", ref, t.Kind, what, s, identifier)
	}

	var out []Check
	for _, col := range t.Columns {
		if err := name("the column", col); err != nil {
			return nil, err
		}
		c := Check{Model: ref, Kind: t.Kind, Column: col}

		var violations string
		switch t.Kind {
		case "not_null":
			violations = fmt.Sprintf("SELECT %s FROM %s WHERE %s IS NULL", col, ref, col)

		case "unique":
			// NULLS ARE NOT DUPLICATES, which is SQL's own answer rather
			// than a choice made here: two NULLs are not equal. A column
			// that must also be present says so with `not_null`, and
			// keeping the two separate is what lets a failure report say
			// which of them went wrong.
			violations = fmt.Sprintf(
				"SELECT %s FROM %s WHERE %s IS NOT NULL GROUP BY %s HAVING COUNT(*) > 1",
				col, ref, col, col)

		case "accepted_values":
			if len(t.Values) == 0 {
				return nil, fmt.Errorf("%s, test `accepted_values` on %s: no values", ref, col)
			}
			lits := make([]string, len(t.Values))
			for i, v := range t.Values {
				lits[i] = d.Literal(v)
			}
			// A NULL IS NOT AN UNEXPECTED VALUE -- it is not_null's
			// business. `col NOT IN (…)` is already unknown for a NULL and
			// returns no row, so the clause changes nothing today; it is
			// written because the day somebody wraps a NOT around this, the
			// behaviour it is relying on would change in silence.
			violations = fmt.Sprintf("SELECT %s FROM %s WHERE %s IS NOT NULL AND %s NOT IN (%s)",
				col, ref, col, col, strings.Join(lits, ", "))

		case "relationships":
			schema, table, dotted := strings.Cut(t.To, ".")
			if !dotted {
				return nil, fmt.Errorf("%s, test `relationships` on %s: `to: %s` has no "+
					"schema; a model is `<schema>.<name>`", ref, col, t.To)
			}
			if err := name("the schema in `to`", schema); err != nil {
				return nil, err
			}
			if err := name("the model in `to`", table); err != nil {
				return nil, err
			}
			if err := name("`field`", t.Field); err != nil {
				return nil, err
			}
			// LEFT JOIN and not NOT IN: a NULL anywhere in the parent's
			// column makes `NOT IN` unknown for every row, so the test
			// would pass by finding nothing -- silently, and exactly when
			// the parent is the one with a problem.
			violations = fmt.Sprintf(
				"SELECT c.%s FROM %s c LEFT JOIN %s p ON c.%s = p.%s "+
					"WHERE c.%s IS NOT NULL AND p.%s IS NULL",
				col, ref, t.To, col, t.Field, col, t.Field)

		default:
			return nil, fmt.Errorf("%s: `%s` is not a test this knows; they are "+
				"not_null, unique, accepted_values and relationships", ref, t.Kind)
		}

		// ONE DEFINITION OF A VIOLATION, counted and sampled from the same
		// text. Two queries written side by side is two things to keep in
		// agreement, and the day they disagree the count says one thing and
		// the example says another.
		c.Count = fmt.Sprintf("SELECT COUNT(*) FROM (%s) AS v", violations)
		if t.Kind != "not_null" {
			c.Sample = fmt.Sprintf("SELECT * FROM (%s) AS v LIMIT 1", violations)
		}
		out = append(out, c)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("%s: test `%s` names no column", ref, t.Kind)
	}
	return out, nil
}
