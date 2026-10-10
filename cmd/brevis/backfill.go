package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// backfillReport says which of four things happened.
//
// `N backfill run(s) queued` was correct and unreadable. N is right -- a slot
// that already has a run is not created twice, which is the right behaviour
// and the reason the number can be small. What it never said is how small out
// of WHAT, and zero is the answer to four different questions:
//
//	the interval is already backfilled   -> the work is DONE
//	the dates are wrong                  -> nothing was asked for
//	the cron never fires in that range   -> nothing was asked for
//	the workflow is the wrong one        -> nothing was asked for
//
// Only the first means success, and it read exactly like the other three. So
// the operator re-reads a command that has no mistake in it, or stops
// believing the number.
//
// It takes the cron because the empty case cannot be explained without it:
// "no slot in that interval" is a puzzle until you can see that the schedule
// fires at 02:00 and the range asked for an afternoon.
func backfillReport(did scheduler.Backfilled, slug, cron, from, to string) string {
	var b strings.Builder
	where := fmt.Sprintf("%s (%s to %s)", slug, from, to)

	switch {
	case did.Slots == 0:
		fmt.Fprintf(&b, "  no slot in that interval for %s\n", where)
		if cron != "" {
			fmt.Fprintf(&b, "  its schedule is `%s`, which never fires between those dates\n", cron)
		}
	case did.Created == 0:
		fmt.Fprintf(&b, "  nothing to do: all %d slot(s) of %s already had a run\n", did.Slots, where)
	case did.Created == did.Slots:
		fmt.Fprintf(&b, "  %d backfill run(s) queued for %s\n", did.Created, where)
	default:
		fmt.Fprintf(&b, "  %d of %d slot(s) queued for %s; %d already had a run\n",
			did.Created, did.Slots, where, did.Existing())
	}
	if did.Created > 0 {
		b.WriteString("  run `brevis scheduler` to execute them\n")
	}
	return b.String()
}

// cronOf reads a workflow's cron for the report, and an empty string is a
// fine answer: this is the sentence that explains an empty interval, not a
// fact the backfill needed. A failure here must not fail a backfill that has
// already happened.
func cronOf(ctx context.Context, pool *postgres.Pool, slug string) string {
	var cron string
	if err := pool.QueryRow(ctx,
		`SELECT cron FROM schedules WHERE workflow_slug = $1`, slug).Scan(&cron); err != nil {
		return ""
	}
	return cron
}
