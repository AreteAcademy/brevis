package check

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
	"github.com/AreteAcademy/brevis/sql/internal/model"
)

var pg = postgres.Dialect{}

func orders() model.Model {
	return model.Model{Schema: "marts", Name: "orders", Materialised: model.Table}
}

func only(t *testing.T, test model.Test) Check {
	t.Helper()
	got, err := For(pg, orders(), test)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d checks, wanted one: %+v", len(got), got)
	}
	return got[0]
}

// A TEST IS A SELECT THAT RETURNS VIOLATING ROWS. Zero rows is a pass, and
// there is nothing else to interpret -- no exit code to read, no count to
// compare against a threshold somebody has to remember.
func TestNotNullSelectsTheNulls(t *testing.T) {
	c := only(t, model.Test{Kind: "not_null", Columns: []string{"order_id"}})
	if !strings.Contains(c.Count, "marts.orders") || !strings.Contains(c.Count, "order_id IS NULL") {
		t.Errorf("%s", c.Count)
	}
	// NOTHING TO SHOW. Every row that breaks not_null breaks it by being
	// NULL, so printing one back teaches nobody anything; the count is the
	// whole of the news.
	if c.Sample != "" {
		t.Errorf("not_null offered a sample value: %s", c.Sample)
	}
}

// ONE CHECK PER COLUMN, because a failure has to name THE column. A single
// check over three columns would say "not_null failed on orders".
func TestNotNullIsOneCheckPerColumn(t *testing.T) {
	got, err := For(pg, orders(), model.Test{
		Kind: "not_null", Columns: []string{"order_id", "customer_id", "ordered_at"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d checks for three columns", len(got))
	}
	for i, want := range []string{"order_id", "customer_id", "ordered_at"} {
		if got[i].Column != want {
			t.Errorf("check %d is about %q, wanted %q", i, got[i].Column, want)
		}
	}
}

// `unique` IGNORES NULLS, which is SQL's own answer: two NULLs are not
// equal, so they are not duplicates. A column that must also be present
// says so with `not_null`, and saying it once in each place is how the two
// stay separable in a failure report.
func TestUniqueGroupsAndIgnoresNulls(t *testing.T) {
	c := only(t, model.Test{Kind: "unique", Columns: []string{"order_id"}})
	for _, want := range []string{"GROUP BY order_id", "HAVING COUNT(*) > 1", "IS NOT NULL"} {
		if !strings.Contains(c.Count, want) {
			t.Errorf("the query has no %q:\n%s", want, c.Count)
		}
	}
	// And it CAN show one: the duplicated value is the news.
	if c.Sample == "" {
		t.Error("unique offers no sample, and the duplicated value is what somebody needs")
	}
}

// `accepted_values` puts the consumer's strings into SQL, through the
// dialect -- which is why Literal is on that interface.
func TestAcceptedValuesQuotesThroughTheDialect(t *testing.T) {
	c := only(t, model.Test{
		Kind: "accepted_values", Columns: []string{"status"},
		Values: []string{"placed", "it's shipped"}})
	if !strings.Contains(c.Count, "NOT IN ('placed', 'it''s shipped')") {
		t.Errorf("not quoted by the dialect:\n%s", c.Count)
	}
	// A NULL IS NOT AN UNEXPECTED VALUE. `col NOT IN (…)` is already
	// unknown for a NULL and returns no row, so the explicit IS NOT NULL
	// changes nothing today -- it is there because the day somebody adds a
	// NOT clause around this, the silent behaviour would change with it.
	if !strings.Contains(c.Count, "IS NOT NULL") {
		t.Errorf("a null is not_null's business, and this does not say so:\n%s", c.Count)
	}
}

// `relationships` is the orphan check: a value here with no row there.
func TestRelationshipsFindsOrphans(t *testing.T) {
	c := only(t, model.Test{
		Kind: "relationships", Columns: []string{"customer_id"},
		To: "staging.stg_customers", Field: "customer_id"})
	for _, want := range []string{"LEFT JOIN staging.stg_customers", "marts.orders"} {
		if !strings.Contains(c.Count, want) {
			t.Errorf("the query has no %q:\n%s", want, c.Count)
		}
	}
	if c.Sample == "" {
		t.Error("the orphan key is what somebody needs and there is no sample")
	}
}

// A NAME THAT IS NOT AN IDENTIFIER IS REFUSED, naming the model and the
// test. These strings reach SQL by concatenation -- there is no placeholder
// on this path, because Conn.Scalar takes a statement and nothing else --
// so the rule is the only thing between a header and the warehouse.
func TestAColumnThatIsNotAnIdentifierIsRefused(t *testing.T) {
	for _, bad := range []model.Test{
		{Kind: "not_null", Columns: []string{"order_id; DROP TABLE x"}},
		{Kind: "unique", Columns: []string{"a b"}},
		{Kind: "not_null", Columns: []string{""}},
		{Kind: "relationships", Columns: []string{"customer_id"},
			To: "staging.stg_customers; DROP TABLE x", Field: "customer_id"},
		{Kind: "relationships", Columns: []string{"customer_id"},
			To: "staging.stg_customers", Field: "x)--"},
	} {
		_, err := For(pg, orders(), bad)
		if err == nil {
			t.Errorf("%+v was accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "marts.orders") {
			t.Errorf("the refusal does not name the model: %v", err)
		}
	}
}

// A VALUE is not an identifier and is NOT refused -- it goes through the
// dialect's Literal, which is what that function is for.
func TestAValueWithPunctuationIsQuotedRatherThanRefused(t *testing.T) {
	c := only(t, model.Test{
		Kind: "accepted_values", Columns: []string{"status"},
		Values: []string{"a'); DROP TABLE x; --"}})
	if !strings.Contains(c.Count, `'a''); DROP TABLE x; --'`) {
		t.Errorf("not quoted:\n%s", c.Count)
	}
}

func TestAnUnknownKindIsRefused(t *testing.T) {
	if _, err := For(pg, orders(), model.Test{Kind: "mutually_exclusive"}); err == nil {
		t.Error("a kind nothing implements was accepted")
	}
}
