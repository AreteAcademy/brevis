package postgres_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// THE SERVER REFUSES WHAT THE TOKENISER CANNOT SEE.
//
// `serve`'s classifier reads a statement and its own comment names the hole:
// `SELECT nextval('s')` is a write, it is spelled as a read, and no classifier
// short of a planner sees inside a function call. Until now the defence there
// was a read-only ROLE, which costs the client a second service account.
//
// This asserts the replacement, against a real server, in both directions --
// because "nextval failed" on its own proves nothing. It fails for a missing
// sequence, for a permission, for a typo. The same statement is therefore run
// OUTSIDE the guard and must SUCCEED: only then is the guard what refused it.
func TestTheServerRefusesAWriteThisServiceCannotSee(t *testing.T) {
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

	guard, guards := conn.(dialect.ReadOnlyGuard)
	if !guards {
		t.Fatal("this dialect offers no read-only guard, so a writable credential is all there is")
	}
	reader, reads := conn.(dialect.Reader)
	if !reads {
		t.Fatal("this dialect cannot read")
	}

	// A sequence of its own, created OUTSIDE the guard -- which also proves
	// the credential this test runs with CAN write, or there would be nothing
	// to demonstrate.
	seq := fmt.Sprintf("brevis_guard_probe_%d", time.Now().UnixNano())
	if err := conn.Exec(ctx, "CREATE SEQUENCE "+seq); err != nil {
		t.Fatalf("creating the probe sequence: %v", err)
	}
	t.Cleanup(func() { _ = conn.Exec(context.Background(), "DROP SEQUENCE IF EXISTS "+seq) })

	advance := dialect.Request{Limit: 1, Query: "SELECT nextval('" + seq + "')"}

	// 1. AN ORDINARY READ STILL WORKS. A guard that refused everything would
	//    satisfy every other assertion in this file.
	var got dialect.Result
	if err := guard.ReadOnly(ctx, func(ctx context.Context) error {
		var e error
		got, e = reader.Read(ctx, dialect.Request{Limit: 1, Query: "SELECT 1 AS n"})
		return e
	}); err != nil {
		t.Fatalf("a plain SELECT inside the guard: %v", err)
	}
	if len(got.Rows) != 1 {
		t.Fatalf("a plain SELECT inside the guard returned %d row(s)", len(got.Rows))
	}

	// 2. AND THE WRITE SPELLED AS A READ DOES NOT.
	err = guard.ReadOnly(ctx, func(ctx context.Context) error {
		_, e := reader.Read(ctx, advance)
		return e
	})
	if err == nil {
		t.Fatal("the server advanced a sequence inside a read-only guard")
	}
	// THE WAREHOUSE'S OWN WORDS. If this refusal ever starts coming from
	// somewhere else -- a missing sequence, a dropped connection -- the test
	// above would still be green and would be testing nothing.
	if !strings.Contains(err.Error(), "read-only transaction") {
		t.Errorf("something other than the read-only rule refused it: %v", err)
	}

	// 3. THE CONTROL. Without the guard the same statement succeeds, so step
	//    2 measured the guard and not the environment.
	if _, err := reader.Read(ctx, advance); err != nil {
		t.Fatalf("outside the guard the same statement failed, so step 2 proved nothing: %v", err)
	}
}
