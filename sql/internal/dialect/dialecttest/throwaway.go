package dialecttest

import (
	"context"
	"fmt"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// MakeThrowawaySchema creates a schema a TEST will build in, and -- where the
// warehouse can be told -- makes what lands in it expire.
//
// ONE PLACE, AND IT IS WHY THIS IS EXPORTED FROM A TEST-SUPPORT PACKAGE. Two
// harnesses create a dataset of their own: this suite, and the runner's
// incremental test. Two spellings of "and also expire it" is how one of them
// stops doing it -- the file that is not being edited today.
//
// WHAT IT PROTECTS AGAINST is a harness that never reaches its own cleanup: a
// panic, a SIGKILL, a laptop that sleeps. `t.Cleanup` runs on a failure and
// not on any of those, and what is left then sits in a customer's warehouse
// beside their real data.
//
// It does NOT remove the leak -- BigQuery has no dataset expiry, only one for
// the tables inside, so a leaked dataset becomes an empty one. That residual
// is named in dialect.Disposable and is the reason this is six lines rather
// than a cleanup framework.
//
// THE EXPIRY GOES SECOND AND IMMEDIATELY. It applies to tables created after
// it, so a helper that set it at the end would leave behind exactly the
// tables the test built.
func MakeThrowawaySchema(ctx context.Context, d dialect.Dialect, conn dialect.Conn,
	schema string) error {

	for _, s := range d.EnsureSchema(schema) {
		if err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("making the schema %s: %w", schema, err)
		}
	}

	// A warehouse with no such idea is not asked. Postgres's is a container,
	// and a statement that did nothing would be a round trip per run with a
	// comment explaining that it is pointless.
	throwaway, can := d.(dialect.Disposable)
	if !can {
		return nil
	}
	if err := conn.Exec(ctx, throwaway.ExpireSchema(schema)); err != nil {
		return fmt.Errorf("making the schema %s throw itself away: %w", schema, err)
	}
	return nil
}
