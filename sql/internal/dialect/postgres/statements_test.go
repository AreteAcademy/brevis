package postgres

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
)

func viewModel() model.Model {
	return model.Model{
		Schema: "staging", Name: "stg_orders", Materialised: model.View,
		SQL: "/* brevis\ntests:\n  - not_null: [id]\n*/\nselect 1 as id",
	}
}

func tableModel() model.Model {
	return model.Model{
		Schema: "marts", Name: "orders", Materialised: model.Table,
		SQL: "select 1 as id",
	}
}

func joined(ss []string) string { return strings.Join(ss, "\n;;\n") }

// A view where nothing is yet: replaced in place, nothing dropped.
//
// CREATE OR REPLACE and not DROP + CREATE, and the choice is conservative on
// purpose: a DROP ... CASCADE would also take an object somebody made by
// hand outside this project, and that is not a thing a build should decide.
// Its cost is named in Build's own comment.
func TestAViewIsReplacedInPlace(t *testing.T) {
	got, err := Dialect{}.Build(viewModel(), dialect.Absent)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d statements, wanted one:\n%s", len(got), joined(got))
	}
	if !strings.HasPrefix(got[0], "CREATE OR REPLACE VIEW staging.stg_orders AS") {
		t.Errorf("not a replace:\n%s", got[0])
	}
	// THE FILE THAT RAN IS THE FILE ON DISK. The header is a comment, so it
	// travels into the DDL and `\sv` in psql shows what the author wrote.
	if !strings.Contains(got[0], "/* brevis") {
		t.Errorf("the header did not survive into the DDL:\n%s", got[0])
	}
}

// A view where a TABLE of that name is: the table goes first, because
// CREATE OR REPLACE VIEW against a table is an error, not a replacement.
func TestAViewOverATableDropsTheTableFirst(t *testing.T) {
	got, err := Dialect{}.Build(viewModel(), dialect.Table)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "DROP TABLE IF EXISTS staging.stg_orders") {
		t.Fatalf("wanted the table dropped first:\n%s", joined(got))
	}
	if strings.Contains(got[0], "CASCADE") {
		t.Error("CASCADE would take an object somebody made outside this project")
	}
}

// A table is rebuilt from its query every time: dropped, then CREATE TABLE AS.
func TestATableIsDroppedAndRecreated(t *testing.T) {
	got, err := Dialect{}.Build(tableModel(), dialect.Table)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d statements, wanted two:\n%s", len(got), joined(got))
	}
	if !strings.HasPrefix(got[0], "DROP TABLE IF EXISTS marts.orders") {
		t.Errorf("first statement:\n%s", got[0])
	}
	if !strings.HasPrefix(got[1], "CREATE TABLE marts.orders AS") {
		t.Errorf("second statement:\n%s", got[1])
	}
}

func TestATableOverAViewDropsTheViewFirst(t *testing.T) {
	got, err := Dialect{}.Build(tableModel(), dialect.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "DROP VIEW IF EXISTS marts.orders") {
		t.Fatalf("wanted the view dropped first:\n%s", joined(got))
	}
}

// IDENTIFIERS ARE NOT QUOTED, and that is a decision rather than an omission.
//
// Postgres folds an unquoted identifier to lower case, and so does the
// reference extractor that built the graph -- `from Staging.Stg_Orders`
// resolves to `staging.stg_orders` there. Quoting here would make the DDL
// case-SENSITIVE while the edges stay case-insensitive, so a model would
// build under a name nothing could read. So a name that is not already a
// plain lower-case identifier is refused, naming the file.
func TestAModelNameThatWouldNeedQuotingIsRefused(t *testing.T) {
	for _, bad := range []model.Model{
		{Schema: "staging", Name: "Stg_Orders", Materialised: model.View, SQL: "select 1"},
		{Schema: "Staging", Name: "stg_orders", Materialised: model.View, SQL: "select 1"},
		{Schema: "staging", Name: "stg-orders", Materialised: model.View, SQL: "select 1"},
		{Schema: "staging", Name: "stg orders", Materialised: model.View, SQL: "select 1"},
	} {
		_, err := Dialect{}.Build(bad, dialect.Absent)
		if err == nil {
			t.Errorf("%s was accepted; Postgres folds it and the graph would not find it", bad.Ref())
			continue
		}
		if !strings.Contains(err.Error(), bad.Ref()) {
			t.Errorf("the refusal does not name it: %v", err)
		}
	}
}

func TestTheSchemaIsMadeIfItIsNotThere(t *testing.T) {
	got := Dialect{}.EnsureSchema("staging")
	if len(got) != 1 || !strings.Contains(got[0], "CREATE SCHEMA IF NOT EXISTS staging") {
		t.Errorf("%v", got)
	}
}

// The query that answers "what is there now", which is what Build is given.
func TestKindOfAsksForTheRelationsKind(t *testing.T) {
	q := Dialect{}.KindOf("staging.stg_orders")
	for _, want := range []string{"staging", "stg_orders"} {
		if !strings.Contains(q, want) {
			t.Errorf("the query does not mention %q:\n%s", want, q)
		}
	}
	// A LITERAL, not a parameter: Conn.Scalar takes a statement and nothing
	// else, and widening it to carry arguments for one caller is how a small
	// interface stops being one. The two halves are matched against
	// pg_catalog by this dialect and are refused above unless they are plain
	// identifiers, so there is nothing here to inject.
	if strings.Contains(q, "$1") {
		t.Error("KindOf expects a parameter and Scalar has nowhere to put one")
	}
}
