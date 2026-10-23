package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

const workflowIndex = "runs_workflow_criado_idx"

// indexDef returns the index's definition and whether it is valid, or "" when
// there is no index by that name.
func indexDef(t *testing.T, pool *postgres.Pool) (string, bool) {
	t.Helper()
	var def string
	var valid bool
	err := pool.QueryRow(context.Background(), `
		SELECT pg_get_indexdef(i.indexrelid), i.indisvalid
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = $1`, workflowIndex).Scan(&def, &valid)
	if err != nil {
		return "", false
	}
	return def, valid
}

func migrate(t *testing.T, direction string) {
	t.Helper()
	if err := postgres.Migrate(context.Background(), os.Getenv("BREVIS_TEST_DATABASE_URL"), direction); err != nil {
		t.Fatalf("migrate %s: %v", direction, err)
	}
}

// #109: the console's reads by workflow scanned the whole of `runs`. The index
// is there, valid, on the two columns they filter and order by.
func TestRunsHaveAnIndexByWorkflowAndTime(t *testing.T) {
	pool := insightsDB(t)
	def, valid := indexDef(t, pool)
	if !valid || !strings.Contains(def, "(workflow_slug, criado_em DESC)") {
		t.Errorf("index = %q (valid %v); wanted runs (workflow_slug, criado_em DESC), valid", def, valid)
	}
}

// Down drops it and up builds it again; the shared test database is left at
// the latest version whatever happens.
func TestTheIndexMigrationGoesDownAndUp(t *testing.T) {
	pool := insightsDB(t)
	t.Cleanup(func() { migrate(t, "up") })

	migrate(t, "down")
	if def, _ := indexDef(t, pool); def != "" {
		t.Errorf("after down the index is still there: %s", def)
	}
	migrate(t, "up")
	if _, valid := indexDef(t, pool); !valid {
		t.Error("after up again the index is missing or invalid")
	}
}

// A CONCURRENTLY build that fails leaves an index of that name behind, and
// goose, which did not record the migration, runs it again. A bare CREATE
// then fails "already exists" on every try, and the installation is stuck
// until somebody drops it by hand. The migration drops before it creates.
func TestAnIndexLeftByAFailedBuildDoesNotStopTheMigration(t *testing.T) {
	pool := insightsDB(t)
	t.Cleanup(func() { migrate(t, "up") })

	migrate(t, "down")
	if _, err := pool.Exec(context.Background(),
		`CREATE INDEX `+workflowIndex+` ON runs (status)`); err != nil {
		t.Fatal(err)
	}
	migrate(t, "up")
	if def, valid := indexDef(t, pool); !valid || !strings.Contains(def, "workflow_slug") {
		t.Errorf("index = %q (valid %v); wanted the leftover replaced by the real one", def, valid)
	}
}

// The planner CAN use it for each shape the console reads by. A test table is
// a few rows, where a sequential scan is the right plan, so sequential scans
// are turned off for the question: does the index fit the query -- the right
// columns, in the right order and direction? The shapes are reads.go's.
func TestTheConsolesReadsByWorkflowCanUseTheIndex(t *testing.T) {
	pool := insightsDB(t)
	ctx := context.Background()
	for name, query := range map[string]string{
		"Workflows' last run": `SELECT id FROM runs WHERE workflow_slug = 'daily'
			ORDER BY criado_em DESC LIMIT 1`,
		"RecentStates": `SELECT workflow_slug, id FROM (
			SELECT workflow_slug, id, row_number() OVER (
				PARTITION BY workflow_slug ORDER BY criado_em DESC, id DESC) AS rn
			FROM runs WHERE workflow_slug = ANY(ARRAY['daily', 'hourly'])) r WHERE rn <= 14`,
		"WorkflowRuns": `SELECT id FROM runs WHERE workflow_slug = 'daily'
			ORDER BY criado_em DESC LIMIT 50`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx, "EXPLAIN "+query)
			if err != nil {
				t.Fatal(err)
			}
			var plan []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, line)
			}
			rows.Close()
			if !strings.Contains(strings.Join(plan, "\n"), workflowIndex) {
				t.Errorf("the plan does not use %s:\n%s", workflowIndex, strings.Join(plan, "\n"))
			}
		})
	}
}
