package bigquery

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
)

func view() model.Model {
	return model.Model{Schema: "staging", Name: "stg_orders",
		Materialised: model.View, SQL: "/* brevis */\nSELECT 1 AS n"}
}

func table() model.Model {
	return model.Model{Schema: "marts", Name: "orders",
		Materialised: model.Table, SQL: "SELECT 1 AS n"}
}

func all(ss []string) string { return strings.Join(ss, "\n;;\n") }

// BigQuery HAS `CREATE OR REPLACE TABLE`, which Postgres does not -- so a
// rebuild is one statement and nothing is ever dropped. The conformance
// suite does not care which; that is the point of having one.
func TestATableIsReplacedInOneStatement(t *testing.T) {
	got, err := Dialect{}.Build(table(), dialect.State{Current: dialect.Table})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d statements, wanted one:\n%s", len(got), all(got))
	}
	if !strings.HasPrefix(got[0], "CREATE OR REPLACE TABLE marts.orders AS") {
		t.Errorf("%s", got[0])
	}
}

// And a view likewise -- BigQuery lets a replacement change the column list,
// which is the limit the Postgres dialect has to name in its own comment.
func TestAViewIsReplacedInOneStatement(t *testing.T) {
	got, err := Dialect{}.Build(view(), dialect.State{Current: dialect.View})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "CREATE OR REPLACE VIEW staging.stg_orders AS") {
		t.Fatalf("%s", all(got))
	}
	if !strings.Contains(got[0], "/* brevis") {
		t.Error("the header did not survive into the DDL")
	}
}

// CHANGING KIND IS THE ONE CASE THAT NEEDS A DROP. `CREATE OR REPLACE VIEW`
// over an existing TABLE is an error in BigQuery, not a replacement, and the
// message is about a name already existing rather than about a kind.
func TestChangingKindDropsTheOtherFirst(t *testing.T) {
	got, err := Dialect{}.Build(view(), dialect.State{Current: dialect.Table})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "DROP TABLE IF EXISTS staging.stg_orders") {
		t.Fatalf("view over a table:\n%s", all(got))
	}

	got, err = Dialect{}.Build(table(), dialect.State{Current: dialect.View})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "DROP VIEW IF EXISTS marts.orders") {
		t.Fatalf("table over a view:\n%s", all(got))
	}
}

// Nothing there: one statement, no drop of something that is not there.
func TestAFirstBuildDropsNothing(t *testing.T) {
	for _, m := range []model.Model{view(), table()} {
		got, err := Dialect{}.Build(m, dialect.State{Current: dialect.Absent})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("%s on a first build:\n%s", m.Ref(), all(got))
		}
	}
}

// THE DIALECT DIFFERENCE WORTH PINNING. BigQuery table and dataset names are
// case-SENSITIVE, and the reference extractor preserves case for this
// dialect -- `from Raw.Orders` stays `Raw.Orders` there and folds to
// `raw.orders` on Postgres. So a mixed-case model builds here and is refused
// on Postgres, and both are correct.
func TestMixedCaseIsAModelNameHere(t *testing.T) {
	m := model.Model{Schema: "Staging", Name: "Stg_Orders",
		Materialised: model.View, SQL: "SELECT 1 AS n"}
	got, err := Dialect{}.Build(m, dialect.State{Current: dialect.Absent})
	if err != nil {
		t.Fatalf("BigQuery keeps the case it was given: %v", err)
	}
	if !strings.Contains(got[0], "Staging.Stg_Orders") {
		t.Errorf("the case was changed:\n%s", got[0])
	}
}

// A name BigQuery itself cannot hold is still refused, naming it.
func TestANameBigQueryCannotHoldIsRefused(t *testing.T) {
	for _, bad := range []model.Model{
		{Schema: "staging", Name: "stg-orders", Materialised: model.View, SQL: "SELECT 1"},
		{Schema: "my dataset", Name: "t", Materialised: model.View, SQL: "SELECT 1"},
		{Schema: "staging", Name: "1st", Materialised: model.View, SQL: "SELECT 1"},
	} {
		if _, err := (Dialect{}).Build(bad, dialect.State{Current: dialect.Absent}); err == nil {
			t.Errorf("%s was accepted", bad.Ref())
		}
	}
}

// KindOf reads INFORMATION_SCHEMA and translates in SQL, so the three
// answers reaching Go are the same three every dialect returns -- BigQuery
// says "BASE TABLE" and "VIEW", and a second translation in Go would be a
// second place for the two to drift.
func TestKindOfTranslatesInSQL(t *testing.T) {
	q := Dialect{}.KindOf("staging.stg_orders")
	for _, want := range []string{"INFORMATION_SCHEMA.TABLES", "staging", "stg_orders", "'view'", "'table'"} {
		if !strings.Contains(q, want) {
			t.Errorf("the query does not contain %q:\n%s", want, q)
		}
	}
	// And the raw words do NOT reach Go.
	if strings.Contains(q, "BASE TABLE") && !strings.Contains(q, "CASE") {
		t.Errorf("the query returns BigQuery's own words untranslated:\n%s", q)
	}
}

func TestTheDatasetIsMadeIfItIsNotThere(t *testing.T) {
	got := Dialect{}.EnsureSchema("staging")
	if len(got) != 1 || !strings.Contains(got[0], "CREATE SCHEMA IF NOT EXISTS staging") {
		t.Errorf("%v", got)
	}
}

func TestNameIsBigquery(t *testing.T) {
	// It is also what the reference extractor is given, so it has to be the
	// string `refs` switches on.
	if got := (Dialect{}).Name(); got != "bigquery" {
		t.Errorf("%q", got)
	}
}
