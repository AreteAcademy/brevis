package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// WHAT A WRITE ANSWERS, ASKED OF POSTGRES.
//
// `CREATE TABLE` and `UPDATE ... WHERE false` both change no rows, and they
// are not the same answer: one made a table and the other matched nothing. A
// console that printed "0 rows" for the first would read as a failure to
// whoever ran it, and they would run it again.
//
// Postgres says which through its command tag -- `UPDATE 2` carries a count
// and `CREATE TABLE` does not -- so this asserts the two apart rather than
// asserting a number.
func TestWhatPostgresSaysAWriteChanged(t *testing.T) {
	dsn := os.Getenv("BREVIS_SQL_IT_DSN")
	if testing.Short() || dsn == "" {
		t.Skip("BREVIS_SQL_IT_DSN not set")
	}
	ctx := context.Background()
	conn, err := postgres.Dialect{}.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	w, can := conn.(dialect.Writer)
	if !can {
		t.Fatal("this dialect cannot run a statement that changes data")
	}
	table := fmt.Sprintf("brevis_write_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })

	for _, c := range []struct {
		why       string
		statement string
		counted   bool
		changed   int64
	}{
		{"DDL counts no rows and did not fail", "CREATE TABLE " + table + " (n int)", false, 0},
		{"an insert counts what it inserted", "INSERT INTO " + table + " (n) VALUES (1),(2),(3)", true, 3},
		{"an update counts what it matched", "UPDATE " + table + " SET n = 9 WHERE n > 1", true, 2},
		{"an update that matched nothing counts ZERO", "UPDATE " + table + " SET n = 0 WHERE n = 4242", true, 0},
		{"a delete counts what it removed", "DELETE FROM " + table + " WHERE n = 9", true, 2},
		{"a truncate counts no rows", "TRUNCATE TABLE " + table, false, 0},
	} {
		t.Run(c.why, func(t *testing.T) {
			got, err := w.Write(ctx, c.statement)
			if err != nil {
				t.Fatalf("running it: %v", err)
			}
			if got.Counted != c.counted {
				t.Errorf("Counted is %v, want %v", got.Counted, c.counted)
			}
			if got.Counted && got.Changed != c.changed {
				t.Errorf("it changed %d row(s), want %d", got.Changed, c.changed)
			}
			// A WRITE RETURNS NO GRID. An empty one and a missing one read
			// the same to a caller, and the console draws a sentence off
			// Counted rather than off the number of rows.
			if len(got.Columns) != 0 || len(got.Rows) != 0 {
				t.Errorf("a write came back with %d column(s) and %d row(s)", len(got.Columns), len(got.Rows))
			}
		})
	}

	// AND A STATEMENT THE SERVER REFUSES IS AN ERROR, not a silent zero.
	if _, err := w.Write(ctx, "UPDATE nope_"+table+" SET n = 1"); err == nil {
		t.Error("a write against a table that is not there came back without complaint")
	}
}
