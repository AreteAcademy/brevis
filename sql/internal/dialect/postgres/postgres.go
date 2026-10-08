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
