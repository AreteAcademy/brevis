package check_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/check"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
	"github.com/AreteAcademy/brevis/sql/internal/model"
	"github.com/AreteAcademy/brevis/sql/internal/project"
	"github.com/AreteAcademy/brevis/sql/internal/run"
)

const schema = "bvs_chk_it"

// THE SAME FIXTURE ON BOTH WAREHOUSES. The four checks are standard SQL and
// nothing in this package is on the Dialect interface except the literal
// quoting, so the two runs must agree -- and when they do not, this is
// where it shows rather than in somebody's project.
//
// The fixture's SQL is written to be the same text on both: `NULLIF('A','A')`
// rather than a bare NULL, because an untyped NULL in a UNION is a
// type-inference question each warehouse answers its own way.
var warehouses = []struct {
	name string
	d    dialect.Dialect
	env  string
}{
	{"postgres", postgres.Dialect{}, "BREVIS_SQL_IT_DSN"},
	{"bigquery", bigquery.Dialect{}, "BREVIS_SQL_IT_BQ_PROJECT"},
}

// Built once and tested, which is the order a consumer runs them in.
func built(t *testing.T, d dialect.Dialect, env string) (context.Context, dialect.Conn, *project.Project, []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipped under -short")
	}
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s not set", env)
	}
	ctx := context.Background()
	conn, err := d.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close(context.Background())
	})

	p, err := project.Load("testdata/project", d.Name())
	if err != nil {
		t.Fatal(err)
	}
	order, err := p.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Build(ctx, d, conn, p, order, run.Options{}); err != nil {
		t.Fatal(err)
	}
	return ctx, conn, p, order
}

// THE FOUR KINDS, AGAINST REAL DATA WITH ONE PLANTED VIOLATION OF EACH.
//
// Writing the SQL is one thing and the warehouse agreeing is another: a
// `unique` that forgot its GROUP BY still parses, and a `relationships`
// written with NOT IN silently passes whenever the parent has a null.
func TestEveryKindFindsItsViolation(t *testing.T) {
	for _, w := range warehouses {
		t.Run(w.name, func(t *testing.T) { everyKindFindsItsViolation(t, w.d, w.env) })
	}
}

func everyKindFindsItsViolation(t *testing.T, d dialect.Dialect, env string) {
	ctx, conn, p, order := built(t, d, env)

	res, err := check.Run(ctx, d, conn, p, order)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ran != 4 {
		t.Fatalf("ran %d checks, the fixture declares 4", res.Ran)
	}
	if len(res.Failed) != 4 {
		t.Fatalf("%d failed, and one of each kind is planted:\n%+v", len(res.Failed), res.Failed)
	}

	kinds := map[string]check.Failure{}
	for _, f := range res.Failed {
		kinds[f.Kind] = f
	}
	for _, want := range []string{"not_null", "unique", "accepted_values", "relationships"} {
		f, found := kinds[want]
		if !found {
			t.Errorf("%s found nothing, and a violation of it is planted", want)
			continue
		}
		if f.Rows != 1 {
			t.Errorf("%s found %d rows, one is planted", want, f.Rows)
		}
	}

	// A FAILURE NAMES THE MODEL, THE COLUMN AND A ROW.
	for kind, wantValue := range map[string]string{
		"unique":          "A",
		"accepted_values": "lost",
		"relationships":   "c9",
	} {
		f := kinds[kind]
		if f.Model != schema+".orders" {
			t.Errorf("%s names the model %q", kind, f.Model)
		}
		if f.Value != wantValue {
			t.Errorf("%s shows %q, and the offending row holds %q", kind, f.Value, wantValue)
		}
	}
	// not_null has no value to show and says so rather than printing NULL.
	if v := kinds["not_null"].Value; v != "" {
		t.Errorf("not_null showed %q", v)
	}
	if c := kinds["not_null"].Column; c != "order_id" {
		t.Errorf("not_null names the column %q", c)
	}
}

// A MODEL THAT IS CLEAN FAILS NOTHING, which is the half that makes the
// other half worth anything: a check that always finds a violation would
// pass this suite and be useless.
func TestACleanModelFailsNothing(t *testing.T) {
	for _, w := range warehouses {
		t.Run(w.name, func(t *testing.T) { aCleanModelFailsNothing(t, w.d, w.env) })
	}
}

func aCleanModelFailsNothing(t *testing.T, d dialect.Dialect, env string) {
	ctx, conn, p, _ := built(t, d, env)

	clean := p.Models[schema+".parents"]
	// NOT not_null: the parent carries a deliberate NULL, which is what
	// makes the relationships mutation killable. unique and
	// accepted_values both ignore a null by design, so they are clean.
	clean.Tests = []model.Test{
		{Kind: "unique", Columns: []string{"customer_id"}},
		{Kind: "accepted_values", Columns: []string{"customer_id"}, Values: []string{"c1", "c2"}},
	}
	p.Models[schema+".parents"] = clean

	res, err := check.Run(ctx, d, conn, p, []string{schema + ".parents"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ran != 2 {
		t.Fatalf("ran %d", res.Ran)
	}
	if len(res.Failed) != 0 {
		t.Errorf("a clean model failed: %+v", res.Failed)
	}
}

// The report is sorted, so two runs over the same project read the same.
func TestTheReportIsInAStableOrder(t *testing.T) {
	ctx, conn, p, order := built(t, postgres.Dialect{}, "BREVIS_SQL_IT_DSN")
	d := postgres.Dialect{}

	var first []string
	for i := range 2 {
		res, err := check.Run(ctx, d, conn, p, order)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range res.Failed {
			got = append(got, fmt.Sprintf("%s/%s/%s", f.Model, f.Kind, f.Column))
		}
		if i == 0 {
			first = got
			if !sort.StringsAreSorted(got) {
				t.Errorf("not sorted: %v", got)
			}
			continue
		}
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Errorf("run 1 %v, run 2 %v", first, got)
		}
	}
}
