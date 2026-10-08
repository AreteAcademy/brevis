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

// ColumnsOf reads the dataset's INFORMATION_SCHEMA.
//
// ONE VALUE, not a row set, so Conn keeps its two methods -- see the
// interface's comment. STRING_AGG with ORDER BY inside it: without the
// ORDER BY the order is whatever the scan produced, and the column list
// feeds a `MERGE ... UPDATE SET`, where a different order on two runs would
// be two different statements for one model.
//
// A relation that is not there yields NULL, which Scalar returns as nil --
// the same "nothing is there" KindOf gives, and the caller never asks for
// the columns of something it has just been told is absent.
func (Dialect) ColumnsOf(ref string) string {
	schema, name, _ := strings.Cut(ref, ".")
	return fmt.Sprintf(
		"SELECT STRING_AGG(column_name, ',' ORDER BY ordinal_position)\n"+
			"  FROM %s.INFORMATION_SCHEMA.COLUMNS\n"+
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
func (d Dialect) Build(m model.Model, st dialect.State) ([]string, error) {
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
	if m.Materialised == model.Incremental {
		// A REBUILD IS A TABLE BUILD, and the DROP is only for a VIEW:
		// `CREATE OR REPLACE TABLE` over an existing view is an error here
		// rather than a replacement, and over an existing table it is the
		// whole statement.
		if st.Rebuild() {
			if st.Current == dialect.View {
				out = append(out, "DROP VIEW IF EXISTS "+ref)
			}
			return append(out, "CREATE OR REPLACE TABLE "+ref+" AS\n"+m.SQL), nil
		}
		return []string{mergeInto(m, st, ref)}, nil
	}
	if m.Materialised == model.Table {
		if st.Current == dialect.View {
			out = append(out, "DROP VIEW IF EXISTS "+ref)
		}
		return append(out, "CREATE OR REPLACE TABLE "+ref+" AS\n"+m.SQL), nil
	}
	if st.Current == dialect.Table {
		out = append(out, "DROP TABLE IF EXISTS "+ref)
	}
	return append(out, "CREATE OR REPLACE VIEW "+ref+" AS\n"+m.SQL), nil
}

// mergeInto is the incremental write.
//
// `QUALIFY` IS THE DEDUPLICATION, and it is the one line that differs from
// the Postgres dialect's version of this function. Postgres has no QUALIFY
// at all -- `syntax error at or near "QUALIFY"`, measured 2026-10-08 -- and
// uses DISTINCT ON; BigQuery has no DISTINCT ON. The two agree on the
// ANSWER, which is what the conformance suite asserts, and on nothing else.
//
// It is needed because the statement will not run otherwise. Two source rows
// for one target row is "UPDATE/MERGE must match at most one source row for
// each target row" -- the rule the spike could only read in the
// documentation, executed here against a real warehouse. Latest by watermark
// wins, the same rule the filter uses.
//
// QUALIFY rides on the filter's own SELECT rather than wrapping it again:
// NewRows already ends in a WHERE, and a second subquery would be a level of
// nesting that buys nothing and shows up in every query log.
//
// The INSERT names the TARGET's columns rather than using `INSERT ROW`.
// `INSERT ROW` would take whatever the model produced in whatever order, so
// a model that grew a column would land it silently into a table of a
// different shape -- or, worse, into the wrong column. Named, BigQuery
// refuses it and says which name it does not have.
func mergeInto(m model.Model, st dialect.State, ref string) string {
	p := dialect.MergeOf(m, st.Columns)
	keys := strings.Join(m.UniqueKey, ", ")

	var b strings.Builder
	fmt.Fprintf(&b, "MERGE INTO %s AS t\nUSING (\n%s\n", ref, dialect.NewRows(m, m.SQL))
	fmt.Fprintf(&b, "QUALIFY ROW_NUMBER() OVER (PARTITION BY %s ORDER BY %s DESC) = 1\n) AS s\n",
		keys, m.Watermark)
	fmt.Fprintf(&b, "   ON %s\n", p.On)
	if p.Set != "" {
		fmt.Fprintf(&b, " WHEN MATCHED THEN UPDATE SET %s\n", p.Set)
	}
	fmt.Fprintf(&b, " WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)", p.Columns, p.Values)
	return b.String()
}
