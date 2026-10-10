package main

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/scheduler"
)

// FOUR SITUATIONS, FOUR SENTENCES.
//
// `N backfill run(s) queued` was correct and unreadable. Zero is what an
// interval already done reports, and also what wrong dates report, and also
// what a cron that never fires in the range reports -- and only one of them
// means "this is finished". The operator either re-reads the command for a
// mistake that is not there, or stops trusting the number.
func TestTheBackfillSaysWhichOfTheFourHappened(t *testing.T) {
	for _, c := range []struct {
		why   string
		did   scheduler.Backfilled
		says  []string
		hides []string
	}{{
		why:  "everything was new",
		did:  scheduler.Backfilled{Slots: 24, Created: 24},
		says: []string{"24", "queued"},
		// With nothing skipped there is nothing to explain, and a sentence
		// about zero existing runs is noise on the common path.
		hides: []string{"already"},
	}, {
		why:  "some of it was already there",
		did:  scheduler.Backfilled{Slots: 24, Created: 3},
		says: []string{"3", "24", "21", "already"},
	}, {
		why: "all of it was already there",
		did: scheduler.Backfilled{Slots: 24, Created: 0},
		// THE ONE THAT MATTERS, and it gets its own sentence rather than
		// the general one. "0 of 24 slot(s) queued" is true and opens with
		// the exact number that reads as a failure; "nothing to do" says
		// the outcome first. Measured: with the dedicated case removed, the
		// general branch still satisfied "24" and "already", so this asserts
		// the wording that makes the difference.
		says:  []string{"nothing to do", "24", "already"},
		hides: []string{"brevis scheduler", "0 of"},
	}, {
		why: "the cron never fires in that interval",
		did: scheduler.Backfilled{Slots: 0, Created: 0},
		// AND NOT "already": nothing was skipped, nothing exists, the dates
		// name no slot at all. Telling somebody their work is done when the
		// interval was empty is the worst of the four.
		says:  []string{"no slot", "0 2 * * *"},
		hides: []string{"already", "brevis scheduler"},
	}} {
		got := backfillReport(c.did, "daily_sales", "0 2 * * *", "2026-01-01", "2026-01-31")
		for _, want := range c.says {
			if !strings.Contains(got, want) {
				t.Errorf("%s: the report does not carry %q:\n%s", c.why, want, got)
			}
		}
		for _, gone := range c.hides {
			if strings.Contains(got, gone) {
				t.Errorf("%s: the report still carries %q:\n%s", c.why, gone, got)
			}
		}
		// THE DATES AND THE WORKFLOW, ALWAYS. Whatever happened, the report
		// has to say what was asked for -- that is half of how somebody sees
		// they typed the wrong month.
		for _, always := range []string{"daily_sales", "2026-01-01", "2026-01-31"} {
			if !strings.Contains(got, always) {
				t.Errorf("%s: the report does not say %q:\n%s", c.why, always, got)
			}
		}
	}
}
