// Package postgres builds models on PostgreSQL.
package postgres

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
)

// Dialect is the PostgreSQL half of building a model.
type Dialect struct{}

// Name is also what the reference extractor is given for this project.
func (Dialect) Name() string { return "postgres" }

// identifier is a name this dialect will write UNQUOTED.
//
// Postgres folds an unquoted identifier to lower case, and so does the
// reference extractor that built the graph: `from Staging.Stg_Orders`
// resolves to `staging.stg_orders` there. Quoting the DDL would make the
// relation case-SENSITIVE while every edge pointing at it stays
// case-insensitive -- so the model would build under a name nothing in the
// project could read, and the failure would be a missing table at run time
// rather than a message now.
var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// EnsureSchema makes the schema, and is safe to run again.
func (Dialect) EnsureSchema(schema string) []string {
	return []string{"CREATE SCHEMA IF NOT EXISTS " + schema}
}

// KindOf asks pg_catalog what the relation is.
//
// `relkind` and not information_schema: a materialised view is `m` there and
// invisible here, and "the build thinks nothing is there and CREATE fails"
// is a worse answer than "something else is there".
func (Dialect) KindOf(ref string) string {
	schema, name, _ := strings.Cut(ref, ".")
	return fmt.Sprintf(`SELECT CASE c.relkind WHEN 'v' THEN 'view' ELSE 'table' END
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = '%s' AND c.relname = '%s'`, schema, name)
}

// ColumnsOf reads information_schema, and here that is the right catalog.
//
// KindOf deliberately uses pg_catalog instead, because a materialised view is
// invisible in information_schema and "nothing is there" is the worst
// possible answer to that question. Columns are a different question: this is
// only ever asked about a table this project built, and information_schema's
// ordinal_position is the portable spelling of the ordering BigQuery uses for
// the same list.
func (Dialect) ColumnsOf(ref string) string {
	schema, name, _ := strings.Cut(ref, ".")
	return fmt.Sprintf(`SELECT string_agg(column_name, ',' ORDER BY ordinal_position)
  FROM information_schema.columns
 WHERE table_schema = '%s' AND table_name = '%s'`, schema, name)
}

// Literal doubles the quote and leaves everything else alone.
//
// Postgres has treated a backslash as an ordinary character since 9.1, when
// standard_conforming_strings became the default -- verified against the
// server this project tests on, not taken from the documentation. So
// escaping one would produce TWO, and a value with a backslash in it would
// stop matching the data it was written for.
//
// A server with standard_conforming_strings OFF is out of scope and says so
// rather than being guessed at: that setting is twelve years deprecated,
// and a dialect quietly producing different SQL per session variable is
// worse than one that is wrong in a documented way.
func (Dialect) Literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Build is every statement that turns the model into what its header says.
//
// A VIEW IS REPLACED IN PLACE, never dropped, and that is conservative on
// purpose. `DROP VIEW ... CASCADE` would also take anything depending on it
// -- including a view somebody wrote by hand outside this project -- and a
// build deciding that on its own is the kind of silent loss this repository
// refuses elsewhere.
//
// WHAT IT COSTS, said where somebody hitting it will look: `CREATE OR
// REPLACE VIEW` cannot change the view's column names or drop a column, so a
// model whose SELECT list changed shape fails with Postgres's own message
// ("cannot change name of view column ..."). Dropping it by hand is the way
// through today; `--full-refresh` is the way through when it exists.
//
// A TABLE IS REBUILT, because `CREATE TABLE AS` is the only way to get the
// new rows and Postgres has no `CREATE OR REPLACE TABLE`. The drop carries no
// CASCADE either, so a view reading this table blocks the rebuild and
// Postgres says which view -- rather than the build removing it.
func (d Dialect) Build(m model.Model, st dialect.State) ([]string, error) {
	for _, part := range []string{m.Schema, m.Name} {
		if !identifier.MatchString(part) {
			return nil, fmt.Errorf("%s cannot be built on postgres: %q has to match %s. "+
				"Postgres folds an unquoted name to lower case and so does the reference "+
				"extractor, so a name needing quotes would build under something no model "+
				"could read. Rename the %s",
				m.Ref(), part, identifier, map[bool]string{true: "directory", false: "file"}[part == m.Schema])
		}
	}
	ref := m.Ref()

	var out []string
	switch m.Materialised {
	case model.Incremental:
		// A REBUILD IS A TABLE BUILD. Nothing is there to add to -- or a
		// `--full-refresh` said to pretend so -- and from tomorrow the
		// table that lands here is what the merge adds to.
		if st.Rebuild() {
			if st.Current == dialect.View {
				out = append(out, "DROP VIEW IF EXISTS "+ref)
			} else {
				out = append(out, "DROP TABLE IF EXISTS "+ref)
			}
			return append(out, "CREATE TABLE "+ref+" AS\n"+m.SQL), nil
		}
		return []string{mergeInto(m, st, ref)}, nil

	case model.Table:
		// ONE drop, chosen by what is actually there. Emitting both would
		// mean a statement that can never do anything on either branch, and
		// a statement that cannot act reads like a rule and is not one.
		if st.Current == dialect.View {
			out = append(out, "DROP VIEW IF EXISTS "+ref)
		} else {
			out = append(out, "DROP TABLE IF EXISTS "+ref)
		}
		out = append(out, "CREATE TABLE "+ref+" AS\n"+m.SQL)
	default:
		if st.Current == dialect.Table {
			out = append(out, "DROP TABLE IF EXISTS "+ref)
		}
		out = append(out, "CREATE OR REPLACE VIEW "+ref+" AS\n"+m.SQL)
	}
	return out, nil
}

// mergeInto is the incremental write.
//
// `MERGE` AND NOT `INSERT ... ON CONFLICT`, which decides the floor: MERGE
// arrived in Postgres 15. The spike measured why -- `ON CONFLICT DO UPDATE`
// REFUSES without a unique index on the key, so supporting the older form
// means telling a consumer to create an index this tool never asked for and
// does not maintain. MERGE needs no index at all.
//
// `DISTINCT ON` IS THE DEDUPLICATION, and it is here rather than in the
// shared helper because Postgres has no QUALIFY -- `QUALIFY row_number() ...`
// is a syntax error here and the idiomatic spelling on BigQuery, both
// measured 2026-10-08. The two dialects agree on the ANSWER, which is what
// the conformance suite asserts, and not on the words.
//
// It is needed at all because the MERGE will not run otherwise: two source
// rows for one target row is "MERGE command cannot affect row a second
// time", and a source that re-emits a corrected row produces exactly that.
// Latest by watermark wins, which is the same rule the filter uses.
//
// WHEN MATCHED IS OMITTED when every column is a key: there is nothing left
// to set, `SET` with no assignment does not parse, and "the row is already
// exactly this row" is not a failure.
func mergeInto(m model.Model, st dialect.State, ref string) string {
	p := dialect.MergeOf(m, st.Columns)
	keys := strings.Join(m.UniqueKey, ", ")

	var b strings.Builder
	fmt.Fprintf(&b, "MERGE INTO %s AS t\nUSING (\n", ref)
	fmt.Fprintf(&b, "  SELECT DISTINCT ON (%s) * FROM (\n%s\n  ) AS brevis_src\n",
		keys, dialect.NewRows(m, m.SQL))
	fmt.Fprintf(&b, "   ORDER BY %s, %s DESC\n) AS s\n", keys, m.Watermark)
	fmt.Fprintf(&b, "   ON %s\n", p.On)
	if p.Set != "" {
		fmt.Fprintf(&b, " WHEN MATCHED THEN UPDATE SET %s\n", p.Set)
	}
	fmt.Fprintf(&b, " WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)", p.Columns, p.Values)
	return b.String()
}
