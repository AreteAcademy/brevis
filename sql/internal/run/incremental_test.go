package run_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/model"
	"github.com/AreteAcademy/brevis/sql/internal/project"
	"github.com/AreteAcademy/brevis/sql/internal/run"
)

// THE ISSUE'S OWN CRITERION: "over three runs on a seeded table, an
// incremental model processes only new rows".
//
// Through the RUNNER and not through the dialect, which the conformance
// suite already covers. What this adds is the loop: that run.Build asks
// StateOf the right question for an incremental model, counts its rows, and
// reads its watermark after rather than before.
//
// THE PROOF IS THE WATERMARK AND NOT THE COUNT. A count of 3 after a run
// that rebuilt the whole model in full is the same 3 a correct incremental
// build leaves -- the two are indistinguishable from the outside, which is
// how this materialisation ships broken and saves nobody anything. So the
// source carries a row that would come back WRONG if it were reprocessed:
// its value in the target was changed by hand between runs, and a rebuild
// would overwrite that change.
func TestThreeRunsProcessOnlyWhatIsNew(t *testing.T) {
	ctx, d, conn := live(t)
	seed(t, ctx, conn)

	p := incProject(t)
	order := []string{schema + ".orders"}

	// Run 1: nothing is there, so everything is.
	res := buildInc(t, ctx, d, conn, p, order, run.Options{})
	if got := rowsOf(t, res); got != 2 {
		t.Fatalf("run 1 landed %d rows, wanted 2", got)
	}
	if res.Built[0].Was != dialect.Absent {
		t.Errorf("run 1 built over %q, wanted nothing", res.Built[0].Was)
	}
	if res.Built[0].Watermark == "" {
		t.Error("run 1 published no watermark")
	}

	// A HAND-MADE CHANGE THE SOURCE DOES NOT KNOW ABOUT. A full rebuild
	// erases it; an incremental build leaves it, because that row is not new.
	exec(t, ctx, conn, "UPDATE "+schema+".orders SET v = 'touched' WHERE k = 1")

	// Run 2: one new row, with a later watermark.
	exec(t, ctx, conn, "INSERT INTO "+schema+".src VALUES (3, 'c', TIMESTAMP '2026-01-03')")
	res = buildInc(t, ctx, d, conn, p, order, run.Options{})
	if got := rowsOf(t, res); got != 3 {
		t.Fatalf("run 2 landed %d rows, wanted 3", got)
	}
	if v := valueOf(t, ctx, conn, 1); v != "touched" {
		t.Errorf("k=1 is %q: run 2 reprocessed a row that was not new", v)
	}

	// Run 3: nothing new at all.
	before := res.Built[0].Watermark
	res = buildInc(t, ctx, d, conn, p, order, run.Options{})
	if got := rowsOf(t, res); got != 3 {
		t.Fatalf("run 3 landed %d rows, wanted the same 3", got)
	}
	if v := valueOf(t, ctx, conn, 1); v != "touched" {
		t.Errorf("k=1 is %q: run 3 reprocessed everything", v)
	}
	if res.Built[0].Watermark != before {
		t.Errorf("the watermark moved on a run with nothing new: %q -> %q",
			before, res.Built[0].Watermark)
	}

	// And a full refresh DOES erase it, which is the difference the flag buys.
	res = buildInc(t, ctx, d, conn, p, order, run.Options{FullRefresh: true})
	if v := valueOf(t, ctx, conn, 1); v != "a" {
		t.Errorf("k=1 is %q after --full-refresh, wanted the source's own 'a'", v)
	}
	if got := rowsOf(t, res); got != 3 {
		t.Errorf("a full refresh landed %d rows, wanted 3", got)
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

// ---------------------------------------------------------------------------

func seed(t *testing.T, ctx context.Context, conn dialect.Conn) {
	t.Helper()
	for _, s := range []string{
		"CREATE SCHEMA IF NOT EXISTS " + schema,
		"DROP TABLE IF EXISTS " + schema + ".src",
		"CREATE TABLE " + schema + ".src (k int, v text, w timestamptz)",
		"INSERT INTO " + schema + ".src VALUES " +
			"(1, 'a', TIMESTAMP '2026-01-01'), (2, 'b', TIMESTAMP '2026-01-02')",
		"DROP TABLE IF EXISTS " + schema + ".orders",
	} {
		exec(t, ctx, conn, s)
	}
}

func exec(t *testing.T, ctx context.Context, conn dialect.Conn, s string) {
	t.Helper()
	if err := conn.Exec(ctx, s); err != nil {
		t.Fatalf("%s\n\n%v", s, err)
	}
}

func valueOf(t *testing.T, ctx context.Context, conn dialect.Conn, k int) string {
	t.Helper()
	v, err := conn.Scalar(ctx, fmt.Sprintf("SELECT v FROM %s.orders WHERE k = %d", schema, k))
	if err != nil {
		t.Fatalf("reading k=%d: %v", k, err)
	}
	return fmt.Sprint(v)
}

func rowsOf(t *testing.T, res run.Result) int64 {
	t.Helper()
	if len(res.Built) != 1 || res.Built[0].Rows == nil {
		t.Fatalf("built %+v", res.Built)
	}
	return *res.Built[0].Rows
}

func buildInc(t *testing.T, ctx context.Context, d dialect.Dialect, conn dialect.Conn,
	p *project.Project, order []string, opt run.Options) run.Result {
	t.Helper()
	res, err := run.Build(ctx, d, conn, p, order, opt)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	return res
}

// incProject is one incremental model over the seeded table, built in Go
// rather than in testdata: the schema name is a constant this file owns, and
// a fixture on disk would have to spell it a second time.
func incProject(t *testing.T) *project.Project {
	t.Helper()
	m := model.Model{
		Schema: schema, Name: "orders", Materialised: model.Incremental,
		UniqueKey: []string{"k"}, Watermark: "w",
		SQL: "SELECT k, v, w FROM " + schema + ".src",
	}
	return &project.Project{Models: map[string]model.Model{m.Ref(): m}}
}
