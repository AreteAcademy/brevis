package run_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/dialecttest"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
	"github.com/AreteAcademy/brevis/sql/internal/model"
	"github.com/AreteAcademy/brevis/sql/internal/project"
	"github.com/AreteAcademy/brevis/sql/internal/run"
)

// warehouse is one live connection and a schema of its own.
type warehouse struct {
	ctx    context.Context
	d      dialect.Dialect
	conn   dialect.Conn
	schema string
}

// open connects to one dialect, or skips saying which variable is missing.
//
// GATED ON ITS OWN VARIABLE PER DIALECT, which is the rule the conformance
// suites already follow: pointing a shell at one must never silently run the
// other against a warehouse somebody is paying for.
//
// A SCHEMA PER RUN, named from the clock, so two runs never meet and a
// failure leaves something to look at without blocking the next one.
func open(t *testing.T, name string) *warehouse {
	t.Helper()
	if testing.Short() {
		t.Skip("skipped under -short")
	}

	var d dialect.Dialect
	var dsn string
	switch name {
	case "postgres":
		d, dsn = postgres.Dialect{}, os.Getenv("BREVIS_SQL_IT_DSN")
		if dsn == "" {
			t.Skip("BREVIS_SQL_IT_DSN not set")
		}
	case "bigquery":
		d, dsn = bigquery.Dialect{}, os.Getenv("BREVIS_SQL_IT_BQ_PROJECT")
		if dsn == "" {
			t.Skip("BREVIS_SQL_IT_BQ_PROJECT not set")
		}
	default:
		t.Fatalf("no such dialect: %s", name)
	}

	ctx := context.Background()
	conn, err := d.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("%s: connecting: %v", name, err)
	}
	w := &warehouse{ctx: ctx, d: d, conn: conn,
		schema: fmt.Sprintf("bvs_inc_%d", time.Now().UnixNano())}

	// THROUGH THE SHARED HELPER, so this harness cannot be the one that
	// forgets to make its dataset disposable. See dialecttest.
	if err := dialecttest.MakeThrowawaySchema(ctx, d, conn, w.schema); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_ = conn.Exec(bg, "DROP SCHEMA IF EXISTS "+w.schema+" CASCADE")
		_ = conn.Close(bg)
	})
	return w
}

// source replaces the seeded table, THROUGH THE DIALECT.
//
// No `CREATE TABLE (k int, v text, w timestamptz)` anywhere in this file:
// those are Postgres types and BigQuery wants INT64, STRING and TIMESTAMP.
// Built as a table model, each dialect writes its own DDL and this test stays
// about the runner.
func (w *warehouse) source(t *testing.T, rows string) {
	t.Helper()
	w.buildOne(t, model.Model{
		Schema: w.schema, Name: "src", Materialised: model.Table, SQL: rows,
	}, run.Options{})
}

// build runs the incremental model the way `brevis-sql build` does.
func (w *warehouse) build(t *testing.T, opt run.Options) run.Result {
	t.Helper()
	return w.buildOne(t, model.Model{
		Schema: w.schema, Name: "orders", Materialised: model.Incremental,
		UniqueKey: []string{"k"}, Watermark: "w",
		SQL: "SELECT k, v, w FROM " + w.schema + ".src",
	}, opt)
}

func (w *warehouse) buildOne(t *testing.T, m model.Model, opt run.Options) run.Result {
	t.Helper()
	p := &project.Project{Models: map[string]model.Model{m.Ref(): m}}
	res, err := run.Build(w.ctx, w.d, w.conn, p, []string{m.Ref()}, opt)
	if err != nil {
		t.Fatalf("building %s: %v", m.Ref(), err)
	}
	return res
}

func (w *warehouse) exec(t *testing.T, s string) {
	t.Helper()
	if err := w.conn.Exec(w.ctx, s); err != nil {
		t.Fatalf("%s\n\n%v", s, err)
	}
}

func (w *warehouse) valueOf(t *testing.T, k int) string {
	t.Helper()
	v, err := w.conn.Scalar(w.ctx,
		fmt.Sprintf("SELECT v FROM %s.orders WHERE k = %d", w.schema, k))
	if err != nil {
		t.Fatalf("reading k=%d: %v", k, err)
	}
	return fmt.Sprint(v)
}

// The literals are spelled once: both warehouses take this exact form.
func row(k int, v, ts string) string {
	return fmt.Sprintf("SELECT %d AS k, '%s' AS v, TIMESTAMP '%s' AS w", k, v, ts)
}

func rowsOf(t *testing.T, res run.Result) int64 {
	t.Helper()
	if len(res.Built) != 1 || res.Built[0].Rows == nil {
		t.Fatalf("built %+v", res.Built)
	}
	return *res.Built[0].Rows
}

// THE ISSUE'S OWN CRITERION: "over three runs on a seeded table, an
// incremental model processes only new rows, ON BOTH DIALECTS".
//
// Through the RUNNER and not through the dialect, which the conformance suite
// already covers at eight cases each. What this adds is the loop: that
// run.Build asks StateOf the right question for an incremental model, counts
// its rows, and reads its watermark after rather than before.
//
// THE PROOF IS NOT THE COUNT. Three rows after a run that rebuilt everything
// is the same three a correct incremental build leaves -- indistinguishable
// from outside, which is how this materialisation ships broken and saves
// nobody anything. So a row in the target is changed BY HAND between runs and
// has to survive: a rebuild erases it, an incremental build does not.
func TestThreeRunsProcessOnlyWhatIsNew(t *testing.T) {
	for _, name := range []string{"postgres", "bigquery"} {
		t.Run(name, func(t *testing.T) {
			w := open(t, name)

			// Run 1: nothing is there, so everything is.
			w.source(t, strings.Join([]string{
				row(1, "a", "2026-01-01 00:00:00"),
				row(2, "b", "2026-01-02 00:00:00"),
			}, "\nUNION ALL\n"))
			res := w.build(t, run.Options{})

			if got := rowsOf(t, res); got != 2 {
				t.Fatalf("run 1 landed %d rows, wanted 2", got)
			}
			if res.Built[0].Was != dialect.Absent {
				t.Errorf("run 1 built over %q, wanted nothing", res.Built[0].Was)
			}
			if res.Built[0].Watermark == "" {
				t.Error("run 1 published no watermark")
			}

			// A HAND-MADE CHANGE THE SOURCE DOES NOT KNOW ABOUT. A full
			// rebuild erases it; an incremental build leaves it, because that
			// row is not new.
			w.exec(t, fmt.Sprintf(
				"UPDATE %s.orders SET v = 'touched' WHERE k = 1", w.schema))

			// Run 2: one new row, with a later watermark.
			w.source(t, strings.Join([]string{
				row(1, "a", "2026-01-01 00:00:00"),
				row(2, "b", "2026-01-02 00:00:00"),
				row(3, "c", "2026-01-03 00:00:00"),
			}, "\nUNION ALL\n"))
			res = w.build(t, run.Options{})

			if got := rowsOf(t, res); got != 3 {
				t.Fatalf("run 2 landed %d rows, wanted 3", got)
			}
			if v := w.valueOf(t, 1); v != "touched" {
				t.Errorf("k=1 is %q: run 2 reprocessed a row that was not new", v)
			}

			// Run 3: nothing new at all.
			before := res.Built[0].Watermark
			res = w.build(t, run.Options{})

			if got := rowsOf(t, res); got != 3 {
				t.Fatalf("run 3 landed %d rows, wanted the same 3", got)
			}
			if v := w.valueOf(t, 1); v != "touched" {
				t.Errorf("k=1 is %q: run 3 reprocessed everything", v)
			}
			if res.Built[0].Watermark != before {
				t.Errorf("the watermark moved on a run with nothing new: %q -> %q",
					before, res.Built[0].Watermark)
			}

			// And a full refresh DOES erase it, which is the difference the
			// flag buys.
			res = w.build(t, run.Options{FullRefresh: true})
			if v := w.valueOf(t, 1); v != "a" {
				t.Errorf("k=1 is %q after --full-refresh, wanted the source's own 'a'", v)
			}
			if got := rowsOf(t, res); got != 3 {
				t.Errorf("a full refresh landed %d rows, wanted 3", got)
			}
		})
	}
}

// A VIEW IS STILL NOT COUNTED. The count moved from `== Table` to `!= View`
// so an incremental model gets one; a mutation that made it unconditional
// would ask a view for its rows -- a number about the SOURCE, which changes
// without this model being rebuilt.
func TestAViewStillReportsNoRowCount(t *testing.T) {
	ctx, d, conn := live(t)
	p := load(t)
	order, err := p.Select("")
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Build(ctx, d, conn, p, order, run.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range res.Built {
		if b.Kind == model.View && b.Rows != nil {
			t.Errorf("%s is a view and reported %d rows", b.Ref, *b.Rows)
		}
		if b.Kind != model.Incremental && b.Watermark != "" {
			t.Errorf("%s is a %s and published a watermark %q", b.Ref, b.Kind, b.Watermark)
		}
	}
}
