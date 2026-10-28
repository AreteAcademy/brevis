package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// beforeTheFeed is the last migration before the events feed's two.
const beforeTheFeed = 25

// feedIndexes are the feed's three reads, each newest first: the events, and
// the runs by when they started and by when they finished.
var feedIndexes = map[string]string{
	"events_at_idx":     "ON public.events USING btree (at DESC, id DESC)",
	"runs_started_idx":  "ON public.runs USING btree (iniciado_em DESC, id DESC) WHERE (iniciado_em IS NOT NULL)",
	"runs_finished_idx": "ON public.runs USING btree (terminado_em DESC, id DESC) WHERE (terminado_em IS NOT NULL)",
}

func indexNamed(t *testing.T, pool *postgres.Pool, name string) (string, bool) {
	t.Helper()
	var def string
	var valid bool
	if err := pool.QueryRow(context.Background(), `
		SELECT pg_get_indexdef(i.indexrelid), i.indisvalid
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = $1`, name).Scan(&def, &valid); err != nil {
		return "", false
	}
	return def, valid
}

// THE FEED HAS A TABLE AND THREE INDEXES, valid, on the columns it orders by.
func TestTheFeedHasItsTableAndIndexes(t *testing.T) {
	pool := insightsDB(t)
	for name, want := range feedIndexes {
		if def, valid := indexNamed(t, pool, name); !valid || !strings.Contains(def, want) {
			t.Errorf("%s = %q (valid %v); want %s", name, def, valid, want)
		}
	}
}

// Down removes them and up builds them again; the shared test database is
// left at the latest version whatever happens.
func TestTheFeedsMigrationsGoDownAndUp(t *testing.T) {
	pool := insightsDB(t)
	t.Cleanup(func() { migrate(t, "up") })

	if err := postgres.MigrateDownTo(context.Background(), os.Getenv("BREVIS_TEST_DATABASE_URL"), beforeTheFeed); err != nil {
		t.Fatalf("migrate down to %d: %v", beforeTheFeed, err)
	}
	for name := range feedIndexes {
		if def, _ := indexNamed(t, pool, name); def != "" {
			t.Errorf("after down %s is still there: %s", name, def)
		}
	}
	var events *string
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('events')::text`).Scan(&events); err != nil || events != nil {
		t.Errorf("after down the events table is still there (%v)", err)
	}
	migrate(t, "up")
	for name := range feedIndexes {
		if _, valid := indexNamed(t, pool, name); !valid {
			t.Errorf("after up again %s is missing or invalid", name)
		}
	}
}
