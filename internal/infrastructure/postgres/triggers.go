package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// Subscription is one workflow and what starts it.
type Subscription struct {
	Slug    string
	Trigger wf.Trigger
}

// Subscriptions are the workflows that declare a trigger.
//
// READ FROM THE PUBLISHED DOCUMENT, and only from there. `definicao` is what
// `publish` stored, so the trigger the scheduler acts on is the one in the
// file that was published -- a column of its own would be a second place for
// it to disagree with the document.
//
// FILTERED IN SQL AND NOT IN GO. Almost no workflow has a trigger, and
// walking every definition to find the two that do would read the whole
// table every cycle. The partial index in 00016 serves exactly this WHERE.
//
// THE CASE IS AT THE SOURCE, not a WHERE the planner may evaluate late --
// the rule 00013 states for the same reason. A workflow with no trigger
// stores `"OnLanded": null`, Go's spelling of a nil slice, and
// `jsonb_array_length` on a scalar RAISES rather than returning zero.
func (r *WorkflowRepo) Subscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT slug, definicao -> 'Trigger'
		FROM workflows
		WHERE CASE WHEN jsonb_typeof(definicao -> 'Trigger' -> 'OnLanded') = 'array'
		           THEN jsonb_array_length(definicao -> 'Trigger' -> 'OnLanded') > 0
		           ELSE false END`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Subscription
	for rows.Next() {
		var s Subscription
		var raw []byte
		if err := rows.Scan(&s.Slug, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.Trigger); err != nil {
			// NAMED AND FATAL for this cycle rather than skipped: a trigger
			// that cannot be read is a workflow that will never fire, and
			// dropping it here would make that silent.
			return nil, fmt.Errorf("workflow %q: reading its trigger: %w", s.Slug, err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Landed is one landing, as the trigger loop needs it.
//
// Four fields and not the row: the loop matches a target, cuts a window from
// an instant, and names the writer in the refusal when a workflow would
// trigger itself. Rows and bytes are the catalog's business.
type Landed struct {
	Target   string
	Workflow string
	LoadedAt time.Time
	Recorded time.Time
}

// LandingsSince reads everything the engine recorded after `cursor`.
//
// BY recorded_at AND NEVER BY loaded_at, which is why 00015 exists:
// `loaded_at` is the step's own clock and a landing dated before the cursor
// is an ordinary thing, not a broken one.
//
// The caller passes a cursor already moved BACK by an overlap. A row whose
// transaction began before the cursor and committed after it is behind a
// cursor already advanced, and no column fixes that -- the overlap plus the
// run's unique idempotency key does, by making a second sighting create
// nothing.
func (r *RunRepo) LandingsSince(ctx context.Context, cursor time.Time) ([]Landed, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT target, workflow_slug, loaded_at, recorded_at
		FROM landings
		WHERE recorded_at > $1
		ORDER BY recorded_at`, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Landed
	for rows.Next() {
		var l Landed
		if err := rows.Scan(&l.Target, &l.Workflow, &l.LoadedAt, &l.Recorded); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LandingCursor is how far the landings have been read. `ok` is false when
// nothing has planted one yet, which is not an error: it is the first cycle.
func (r *RunRepo) LandingCursor(ctx context.Context) (time.Time, bool, error) {
	var at time.Time
	err := r.pool.QueryRow(ctx, `SELECT recorded_at FROM landing_cursor`).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return at, err == nil, err
}

// AdvanceLandingCursor moves it, and ONLY FORWARD.
//
// A cycle that read less far than the last one must not rewind it. Rewinding
// would replay a window already seen, and the only reason that would be
// harmless is the idempotency key -- relying on that for CORRECTNESS rather
// than for safety is how a real bug becomes invisible.
func (r *RunRepo) AdvanceLandingCursor(ctx context.Context, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO landing_cursor (id, recorded_at) VALUES (true, $1)
		ON CONFLICT (id) DO UPDATE SET recorded_at = GREATEST(landing_cursor.recorded_at, EXCLUDED.recorded_at)`,
		at)
	return err
}
