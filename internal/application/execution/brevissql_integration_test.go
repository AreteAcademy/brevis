package execution_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// brevis-sql ON /data, through the real pipe.
//
// S7 of #62 is "every materialized model emits `landed` and appears on
// /data", and that is the half that makes brevis-sql part of Brevis rather
// than a SQL runner beside it. Everything up to the printed line is tested
// in that module; what nothing there can test is whether the ENGINE takes
// it -- `sql/` is a module of its own and cannot import a line of this one,
// which is #66's rule and the reason the engine's weight gate stays put.
//
// AND THE GAP HAS NO EDGE. A target the engine does not recognise is
// DROPPED AND COUNTED, never refused: the step succeeds, the tables really
// are built, and the models simply never appear. So this is the only place
// the contract can be checked, and it is checked with the real binary, the
// real executor, an operating-system pipe and a real Postgres.
func TestIntegrationBrevisSQLModelsReachTheCatalog(t *testing.T) {
	dsn := os.Getenv("BREVIS_IT_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_IT_PG_DSN not set")
	}

	ctx := context.Background()
	pool, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	// A SCHEMA PER RUN, and that is not tidiness.
	//
	// The first version used a fixed name and PASSED WITH BOTH MUTATIONS
	// APPLIED -- the target emitted without a scheme, and the `landed` line
	// not printed at all. `landings` is an append-only table: dropping the
	// SQL schema at the end leaves its rows behind, so the catalog still
	// held the targets an earlier, working run had written and the
	// assertion read those. A test that passes on the residue of its own
	// last success is the shape this repository keeps finding.
	schema := fmt.Sprintf("bvs_cat_%d", time.Now().UnixNano())

	dir := t.TempDir()
	binary := buildBrevisSQL(t, dir)
	project := sqlProject(t, dir, schema)

	t.Cleanup(func() {
		back := context.Background()
		_, _ = pool.Exec(back, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		// And the catalog rows, or every run of this test leaves two
		// destinations on a real /data page for ever.
		_, _ = pool.Exec(back, "DELETE FROM landings WHERE target LIKE $1", "%/"+schema+"/%")
	})

	// THE DSN IS A SECRET, DECLARED BY NAME, which is how `--dsn-from` and
	// `secrets:` were always going to meet.
	//
	// The local executor builds an explicit environment and inherits
	// nothing -- "the orchestrator carries credentials a task must not see
	// by accident" -- and `secrets:` is the named opt-in against that rule.
	// So a brevis-sql step in a real workflow names its variable in two
	// places and the value in neither, which is the whole point of the
	// flag taking a NAME.
	t.Setenv("BREVIS_SQL_IT_CATALOG_DSN", dsn)

	runID := uuid.New()
	repo := postgres.NewRunRepo(pool)
	if err := createRun(ctx, pool, runID); err != nil {
		t.Fatalf("creating the run: %v", err)
	}

	r := app.Runner{RunID: runID, Persist: repo, Processo: localExecutor(t)}
	w := wf.Workflow{
		Slug: "e2e",
		Nodes: []wf.Node{{
			ID: "models",
			Run: fmt.Sprintf("%s build --project %s --dsn-from BREVIS_SQL_IT_CATALOG_DSN 2>&1",
				binary, project),
			Secrets: map[string]string{"BREVIS_SQL_IT_CATALOG_DSN": "local"},
		}},
	}
	if err := r.Run(ctx, w); err != nil {
		t.Fatalf("the step failed: %v", err)
	}

	// `postgres://<database>/<schema>/<model>`, which is what the dialect
	// builds from the connection it already has.
	database := databaseOf(t, dsn)
	want := map[string]bool{
		"postgres://" + database + "/" + schema + "/base": false,
		"postgres://" + database + "/" + schema + "/top":  false,
	}

	entries, err := postgres.NewReadRepo(pool).Catalog(ctx)
	if err != nil {
		t.Fatalf("reading the catalog: %v", err)
	}
	for _, e := range entries {
		if _, looking := want[e.Target]; !looking {
			continue
		}
		want[e.Target] = true
		if e.Kind != "postgres" {
			t.Errorf("%s is listed as kind %q", e.Target, e.Kind)
		}
	}
	for target, found := range want {
		if !found {
			t.Errorf("%s built and never reached /data.\n"+
				"    An unrecognised target is dropped and counted, so the step "+
				"succeeded and said nothing.", target)
		}
	}

	// THE TABLE SAYS HOW MANY ROWS AND THE VIEW SAYS NOTHING, which is what
	// `Recent` carries -- a nil there is the absence travelling all the way
	// from the model's header to the page.
	detail, err := postgres.NewReadRepo(pool).CatalogTarget(ctx,
		"postgres://"+database+"/"+schema+"/top")
	if err != nil || detail == nil {
		t.Fatalf("the table is not in the catalog: %v", err)
	}

	// And the marker is not in the step's log; it is a message, not output.
	logs, err := repo.LogsDaRun(ctx, runID)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l.Log, "@brevis:") {
			t.Errorf("the marker ended up in the step's log:\n%s", l.Log)
		}
	}
}

// buildBrevisSQL builds the binary from the module next door.
func buildBrevisSQL(t *testing.T, dir string) string {
	t.Helper()
	output := filepath.Join(dir, "brevis-sql")
	cmd := exec.Command("go", "build", "-o", output, "./cmd/brevis-sql")
	cmd.Dir = "../../../sql"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building brevis-sql: %v\n%s", err, out)
	}
	return output
}

// sqlProject writes the smallest project that exercises both kinds: a view
// and a table, the table reading the view.
func sqlProject(t *testing.T, dir, schema string) string {
	t.Helper()
	root := filepath.Join(dir, "project", "models", schema)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("base.sql", "SELECT 1 AS n\n")
	write("top.sql", "/* brevis\nmaterialized: table\n*/\nSELECT n FROM "+schema+".base\n")
	return filepath.Join(dir, "project")
}

// databaseOf is the database name as the dialect reads it: the last path
// segment of the DSN, before any query string.
func databaseOf(t *testing.T, dsn string) string {
	t.Helper()
	rest := dsn
	if _, after, ok := strings.Cut(rest, "://"); ok {
		rest = after
	}
	rest, _, _ = strings.Cut(rest, "?")
	i := strings.LastIndex(rest, "/")
	if i < 0 {
		t.Fatalf("no database in %q", dsn)
	}
	return rest[i+1:]
}
