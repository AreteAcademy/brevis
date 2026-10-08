package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sch "github.com/AreteAcademy/brevis/internal/domain/schedule"
)

// landingOverlap is how far back of its own cursor each cycle re-reads.
//
// A ROW CAN COMMIT BEHIND AN ADVANCED CURSOR. `recorded_at` is stamped when
// the INSERT runs and the cursor moves when the cycle ends, so a transaction
// that began before the cursor and committed after it is invisible to the
// next poll. No column fixes that; re-reading a minute of history does.
//
// It is free because of the idempotency key: a landing seen twice composes
// the same `slug:landed:window` and `runs.idempotency_key` is UNIQUE, so the
// second sighting creates nothing. That is why the overlap can be generous.
const landingOverlap = time.Minute

// landingCycle turns landings into runs.
//
// IT IS PART OF Cycle AND NOT A LOOP OF ITS OWN. The scheduler already wakes
// on a ticker and already creates runs; a second goroutine with a second
// clock would be a second place for the same race.
//
// A failure on one subscription must not stop the others, which is the rule
// the schedule half already follows for a cron that does not parse.
func (s *Scheduler) landingCycle(ctx context.Context, now time.Time) (int, error) {
	subs, err := s.workflows.Subscriptions(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the subscriptions: %w", err)
	}
	if len(subs) == 0 {
		// NOTHING SUBSCRIBES, SO NOTHING IS READ -- and the cursor is not
		// planted either. A cursor planted on a system with no triggers would
		// be a row that means nothing, and the first workflow to subscribe
		// would inherit a window it never asked for.
		return 0, nil
	}

	cursor, planted, err := s.runs.LandingCursor(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the landing cursor: %w", err)
	}
	if !planted {
		// PLANTED AT now(), NEVER AT THE EPOCH. The same rule
		// `schedules.ultimo_slot` learned the hard way -- a marker left unset
		// makes the first cycle materialise from the beginning of time. Here
		// that would be one run per debounce window for the whole history of
		// the landings table.
		if err := s.runs.AdvanceLandingCursor(ctx, now); err != nil {
			return 0, err
		}
		s.log.Info("landing trigger started", "subscriptions", len(subs),
			"from", now.Format(time.RFC3339))
		return 0, nil
	}

	landings, err := s.runs.LandingsSince(ctx, cursor.Add(-landingOverlap))
	if err != nil {
		return 0, fmt.Errorf("reading the landings: %w", err)
	}
	if len(landings) == 0 {
		return 0, nil
	}

	// GROUPED BEFORE ANYTHING IS CREATED, because the run has to carry EVERY
	// target of its window. Creating on the first match and skipping the rest
	// would start the right run and tell it about one landing out of ten --
	// and the step would rebuild one table of the three that moved.
	type bucket struct {
		slug    string
		window  time.Time
		targets []string
		seen    map[string]bool
	}
	buckets := map[string]*bucket{}
	var order []string

	for _, l := range landings {
		for _, sub := range subs {
			if !matches(sub.Trigger.OnLanded, l.Target) {
				continue
			}
			// A WORKFLOW IS NOT STARTED BY ITS OWN WRITE. Refused here as
			// well as at publish, because a document stored by an older
			// engine or edited in the database never passed the domain's
			// invariants -- and the loop this prevents is one that never
			// stops on its own.
			if sub.Slug == l.Workflow {
				s.log.Warn("a landing was ignored: the workflow writes it itself",
					"workflow", sub.Slug, "target", l.Target)
				continue
			}

			// THE WINDOW IS CUT FROM THE ENGINE'S CLOCK, not the step's.
			// "Ten landings in a minute" is a statement about when they
			// ARRIVED; `loaded_at` is what each step said, and one skewed
			// clock would split a burst into two runs.
			window := l.Recorded.UTC().Truncate(sub.Trigger.Debounce)
			key := sub.Slug + "|" + window.Format(time.RFC3339Nano)

			b := buckets[key]
			if b == nil {
				b = &bucket{slug: sub.Slug, window: window, seen: map[string]bool{}}
				buckets[key] = b
				order = append(order, key)
			}
			// ONE ENTRY PER TARGET, not per landing. Ten landings on one
			// table is one thing to rebuild, and a list naming it ten times
			// would be a list nobody can read.
			if !b.seen[l.Target] {
				b.seen[l.Target] = true
				b.targets = append(b.targets, l.Target)
			}
		}
	}

	var created int
	for _, key := range order {
		b := buckets[key]
		made, err := s.enqueueLanded(ctx, b.slug, b.window, b.targets)
		if err != nil {
			s.log.Error("starting a workflow from a landing",
				"workflow", b.slug, "targets", b.targets, "error", err)
			continue
		}
		if made {
			created++
		}
	}

	// ADVANCED TO WHAT WAS READ, not to `now`. A landing recorded between the
	// query and this line is behind `now` and would be skipped; it is not
	// behind the last row read.
	if err := s.runs.AdvanceLandingCursor(ctx, landings[len(landings)-1].Recorded); err != nil {
		return created, err
	}
	return created, nil
}

// enqueueLanded creates the run for one workflow and one window.
func (s *Scheduler) enqueueLanded(ctx context.Context, slug string, window time.Time,
	targets []string) (bool, error) {
	def, err := s.workflows.Definition(ctx, slug)
	if err != nil {
		return false, err
	}
	bruto, err := json.Marshal(def)
	if err != nil {
		return false, err
	}
	// The DEFAULTS, as a schedule uses: there is nobody to supply values when
	// a table lands at four in the morning.
	defaults, err := def.Resolver(nil)
	if err != nil {
		return false, fmt.Errorf("default params of %q: %w", slug, err)
	}

	// createAndEnqueue is the one that knows: a collision on
	// `slug:landed:window` is the NORMAL outcome here -- it is how ten
	// landings in a window become one run -- and asking the database first
	// would be a second round trip for an answer the insert already gives.
	return s.createAndEnqueue(ctx, slug, bruto, window,
		sch.TriggerLanded, 0, defaults, def.MaxActive, targets...)
}

// matches says whether a landing's target is one of the subscribed ones.
//
// A TRAILING `/*` IS A PREFIX, and the only pattern there is. auto_table
// creates a table per route, so a dataset is the only thing a subscription
// can name for them -- and `bigquery://p/bronze/*` must match
// `bigquery://p/bronze/orders` and NOT `bigquery://p/bronze_raw/orders`,
// which is why the slash stays in the prefix.
func matches(subscribed []string, target string) bool {
	for _, s := range subscribed {
		if prefix, ok := strings.CutSuffix(s, "*"); ok {
			if strings.HasPrefix(target, prefix) {
				return true
			}
			continue
		}
		if s == target {
			return true
		}
	}
	return false
}
