package run_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
	"github.com/AreteAcademy/brevis/sql/internal/project"
	"github.com/AreteAcademy/brevis/sql/internal/run"
)

const schema = "bvs_run_it"

func live(t *testing.T) (context.Context, dialect.Dialect, dialect.Conn) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipped under -short")
	}
	dsn := os.Getenv("BREVIS_SQL_IT_DSN")
	if dsn == "" {
		t.Skip("BREVIS_SQL_IT_DSN not set")
	}
	ctx := context.Background()
	d := postgres.Dialect{}
	conn, err := d.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close(context.Background())
	})
	return ctx, d, conn
}

func load(t *testing.T) *project.Project {
	t.Helper()
	p, err := project.Load("testdata/project", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func reads(t *testing.T, ctx context.Context, conn dialect.Conn, ref string) string {
	t.Helper()
	v, err := conn.Scalar(ctx, "SELECT n FROM "+ref)
	if err != nil {
		t.Fatalf("reading %s: %v", ref, err)
	}
	return fmt.Sprint(v)
}

// DEPENDENCY ORDER, proven by a build that cannot work without it.
//
// `middle` reads `base` and `top` reads `middle`, so a build in any other
// order fails on a relation that does not exist yet. Nothing here asserts
// the order directly -- the server does, by refusing.
func TestBuildRunsInDependencyOrder(t *testing.T) {
	ctx, d, conn := live(t)
	p := load(t)

	order, err := p.Select("")
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Build(ctx, d, conn, p, order)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if len(res.Built) != 3 {
		t.Fatalf("built %d models, wanted 3", len(res.Built))
	}
	if got := reads(t, ctx, conn, schema+".top"); got != "111" {
		t.Errorf("%s.top reads %s, wanted 111 -- 1 + 10 + 100 through the chain", schema, got)
	}
}

// THE SCHEMA IS MADE ONCE, not once per model. Three models share one and
// the statement is in the plan a single time.
func TestTheSchemaIsEnsuredOncePerSchema(t *testing.T) {
	spy := &spyConn{}
	p := load(t)
	order, err := p.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Build(context.Background(), postgres.Dialect{}, spy, p, order); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range spy.ran {
		if strings.HasPrefix(s, "CREATE SCHEMA") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("CREATE SCHEMA ran %d times for one schema:\n%s", n, strings.Join(spy.ran, "\n"))
	}
}

// Rebuilding is a no-op in effect: the same three models, the same values.
func TestBuildingTwiceLeavesTheSameThing(t *testing.T) {
	ctx, d, conn := live(t)
	p := load(t)
	order, _ := p.Select("")

	for i := range 2 {
		if _, err := run.Build(ctx, d, conn, p, order); err != nil {
			t.Fatalf("build %d: %v", i+1, err)
		}
	}
	if got := reads(t, ctx, conn, schema+".top"); got != "111" {
		t.Errorf("after two builds %s.top reads %s", schema, got)
	}
}

// `--select` builds what it names and nothing else.
func TestBuildOnlyWhatSelectNames(t *testing.T) {
	ctx, d, conn := live(t)
	p := load(t)

	// Everything first, so there is something for the narrow build NOT to
	// touch and something for `middle` to read.
	all, _ := p.Select("")
	if _, err := run.Build(ctx, d, conn, p, all); err != nil {
		t.Fatal(err)
	}

	only, err := p.Select(schema + ".middle")
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Build(ctx, d, conn, p, only)
	if err != nil {
		t.Fatalf("a narrow build failed: %v", err)
	}
	if len(res.Built) != 1 || res.Built[0].Ref != schema+".middle" {
		t.Fatalf("built %+v, wanted only %s.middle", res.Built, schema)
	}
	// AND NOTHING DOWNSTREAM WAS LOST. The dialect replaces a view in place
	// and drops nothing, so `top` is still there -- which is the whole
	// reason that choice was made.
	if got := reads(t, ctx, conn, schema+".top"); got != "111" {
		t.Errorf("%s.top reads %s after a narrow build of its upstream", schema, got)
	}
}

// A statement the server refuses stops the build, naming the MODEL. "syntax
// error at or near" with no model name sends somebody to read every file.
func TestAFailureNamesTheModel(t *testing.T) {
	ctx, d, conn := live(t)
	p := load(t)

	broken := p.Models[schema+".middle"]
	broken.SQL = "SELECT this_column_is_not_there FROM bvs_run_it.base"
	p.Models[schema+".middle"] = broken

	order, _ := p.Select("")
	_, err := run.Build(ctx, d, conn, p, order)
	if err == nil {
		t.Fatal("a model the server refused was reported as built")
	}
	if !strings.Contains(err.Error(), schema+".middle") {
		t.Errorf("the failure does not name the model: %v", err)
	}
}

// spyConn records statements and answers every question with "nothing is
// there", which is what a first build sees.
type spyConn struct{ ran []string }

func (s *spyConn) Exec(_ context.Context, statement string) error {
	s.ran = append(s.ran, statement)
	return nil
}
func (s *spyConn) Scalar(context.Context, string) (any, error) { return nil, nil }
func (s *spyConn) Target(ref string) string                    { return "spy://db/" + ref }
func (s *spyConn) Close(context.Context) error                 { return nil }

// EVERY MODEL SAYS WHERE IT WROTE, which is the half that makes this part
// of Brevis rather than a SQL runner beside it. The engine takes the target
// on a `landed` line and `/data` lists it.
func TestEveryBuiltModelCarriesItsCatalogTarget(t *testing.T) {
	ctx, d, conn := live(t)
	p := load(t)
	order, _ := p.Select("")

	res, err := run.Build(ctx, d, conn, p, order)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range res.Built {
		want := conn.Target(b.Ref)
		if b.Target != want {
			t.Errorf("%s carries %q, wanted %q", b.Ref, b.Target, want)
		}
	}
}

// A TABLE SAYS HOW MANY ROWS; A VIEW SAYS NOTHING.
//
// `Rows` is a pointer because ABSENT IS NOT ZERO -- the engine's own words
// for its `landed` line. A view holds no rows at all, so reporting 0 would
// put a destination on `/data` that looks like it emptied overnight, and
// the catalog cannot tell that zero from a table that really is empty.
func TestATableReportsItsRowsAndAViewReportsNone(t *testing.T) {
	ctx, d, conn := live(t)
	p := load(t)
	order, _ := p.Select("")

	res, err := run.Build(ctx, d, conn, p, order)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]*int64{}
	for _, b := range res.Built {
		seen[b.Ref] = b.Rows
	}
	if n := seen[schema+".base"]; n != nil {
		t.Errorf("the view %s.base reported %d rows; a view holds none", schema, *n)
	}
	if n := seen[schema+".top"]; n == nil {
		t.Errorf("the table %s.top reported no row count", schema)
	} else if *n != 1 {
		t.Errorf("%s.top holds %d rows, and the fixture makes one", schema, *n)
	}
}
