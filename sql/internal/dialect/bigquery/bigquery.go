// Package bigquery builds models on Google BigQuery.
package bigquery

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
)

// Dialect is the BigQuery half of building a model.
type Dialect struct{}

// Name is also what the reference extractor is given, so it has to be the
// string `refs` switches on.
func (Dialect) Name() string { return "bigquery" }

// identifier is a dataset or table name BigQuery can hold.
//
// MIXED CASE IS ALLOWED HERE AND REFUSED ON POSTGRES, and both are right.
// BigQuery dataset and table names are case-SENSITIVE and the reference
// extractor preserves case for this dialect -- `from Raw.Orders` stays
// `Raw.Orders` here and folds to `raw.orders` there -- so the graph and the
// DDL agree either way. That is the first real difference the Dialect
// interface was written to hold, and it is a naming rule rather than a
// statement, which is why it is not something the conformance suite can see.
//
// (Column names are a different question and BigQuery DOES fold those; see
// the SDK's #43. Nothing here names a column.)
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maxIdentifier is BigQuery's own limit, checked apart from the pattern
// because Go's regexp refuses a repeat count over 1000 and bending the
// number to suit the engine would make the code state a limit that is not
// the warehouse's.
const maxIdentifier = 1024

// EnsureSchema makes the DATASET. BigQuery's `SCHEMA` is a dataset, and the
// word in the DDL is the one BigQuery uses.
func (Dialect) EnsureSchema(schema string) []string {
	return []string{"CREATE SCHEMA IF NOT EXISTS " + schema}
}

// KindOf reads the dataset's INFORMATION_SCHEMA.
//
// TRANSLATED IN SQL, not in Go. BigQuery says `BASE TABLE` and `VIEW`;
// Postgres says `r` and `v`. Doing the mapping in each dialect's query means
// the three answers reaching Go are the same three everywhere, and
// dialect.KindFrom stays a function about drivers rather than about
// warehouses.
//
// Anything that is not a VIEW is reported as a table, deliberately: an
// external or materialised view is not something this tool made, and
// answering "nothing is there" would send a CREATE at it.
func (Dialect) KindOf(ref string) string {
	schema, name, _ := strings.Cut(ref, ".")
	return fmt.Sprintf(
		"SELECT CASE table_type WHEN 'VIEW' THEN 'view' ELSE 'table' END\n"+
			"  FROM %s.INFORMATION_SCHEMA.TABLES\n"+
			" WHERE table_name = '%s'", schema, name)
}

// Literal escapes with a BACKSLASH, which is the whole difference.
//
// Two measurements on 2026-10-08, both against the real warehouse:
//
//   - a backslash is an ESCAPE here and a character on Postgres.
//     `SELECT LENGTH('a\bc')` is 3 here and 4 there, so one left alone
//     turns `accepted_values: ["a\bc"]` into a test that accepts a value
//     nobody wrote.
//   - a doubled quote is NOT an escape here. `'it”s'` is read as two
//     literals written next to each other, and BigQuery refuses it:
//     "concatenated string literals must be separated by whitespace".
//     Postgres takes exactly that form, and it is what the first version
//     of this function did -- copied from the Postgres one, passing every
//     unit test of its output, and failing the conformance suite against
//     the warehouse on the first value with an apostrophe in it.
//
// ORDER MATTERS: the backslash goes first, or the one added for the quote
// is doubled by the line after it.
//
// Not a raw string (`r'…'`): a raw string cannot contain the quote that
// closes it, so the one escape it removes is the one still needed.
func (Dialect) Literal(s string) string {
	// The backslash FIRST, or every escape added below is doubled by it.
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	// A LITERAL NEWLINE CANNOT SIT IN A QUOTED STRING HERE. Postgres takes
	// one; BigQuery answers "Unclosed string literal", which reads as a bug
	// in the SQL this package wrote rather than as a value with a line
	// break in it. Found by the conformance suite, from a test value that
	// contained a real newline by accident.
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return "'" + s + "'"
}

// Build is every statement that turns the model into what its header says.
//
// ONE STATEMENT WHEN THE KIND DOES NOT CHANGE. BigQuery has `CREATE OR
// REPLACE TABLE` as well as `CREATE OR REPLACE VIEW`, and its replacement
// may change the column list -- both of which Postgres lacks, which is why
// the Postgres dialect drops a table to rebuild it and names a limit about
// view columns that does not exist here.
//
// CHANGING KIND IS THE ONE CASE THAT NEEDS A DROP: `CREATE OR REPLACE VIEW`
// over an existing table is an error in BigQuery rather than a replacement,
// and its message is about a name that already exists rather than about a
// kind, so the drop is here where the reason can be written down.
func (d Dialect) Build(m model.Model, current dialect.Kind) ([]string, error) {
	for _, part := range []string{m.Schema, m.Name} {
		if !identifier.MatchString(part) || len(part) > maxIdentifier {
			return nil, fmt.Errorf("%s cannot be built on bigquery: %q has to match %s "+
				"and be at most %d characters. BigQuery keeps the case it is given, so "+
				"the name may be mixed -- but a dash, a space or a leading digit it "+
				"cannot hold at all", m.Ref(), part, identifier, maxIdentifier)
		}
	}
	ref := m.Ref()

	var out []string
	if m.Materialised == model.Table {
		if current == dialect.View {
			out = append(out, "DROP VIEW IF EXISTS "+ref)
		}
		return append(out, "CREATE OR REPLACE TABLE "+ref+" AS\n"+m.SQL), nil
	}
	if current == dialect.Table {
		out = append(out, "DROP TABLE IF EXISTS "+ref)
	}
	return append(out, "CREATE OR REPLACE VIEW "+ref+" AS\n"+m.SQL), nil
}
