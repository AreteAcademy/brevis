package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/domain/events"
	sch "github.com/AreteAcademy/brevis/internal/domain/schedule"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// WorkflowRepo persiste a definicao publicada de um workflow.
type WorkflowRepo struct{ pool *Pool }

func NewWorkflowRepo(p *Pool) *WorkflowRepo { return &WorkflowRepo{pool: p} }

// Publicar writes the workflow and its schedule in one transaction.
//
// Both things together, and not in separate calls: publishing the graph without
// the schedule would leave a workflow that never fires, and the schedule without
// the graph would make the scheduler create runs for something that does not
// exist.
//
// A publish that changed something -- a new workflow, a new graph, a new
// schedule -- is in the events feed, in the same transaction, with actor as
// who did it. One that changed nothing is not: a CI republishing everything on
// every deploy must not fill the feed.
func (r *WorkflowRepo) Publicar(ctx context.Context, w wf.Workflow, projeto uuid.UUID, actor string) error {
	def, err := json.Marshal(w)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do commit

	// What is published now, read under the row's lock so a concurrent
	// publish of the same workflow compares against this one's result.
	var exists, graphChanged, scheduleChanged bool
	err = tx.QueryRow(ctx, `
		SELECT true, w.definicao IS DISTINCT FROM $3::jsonb,
		       s.cron IS DISTINCT FROM NULLIF($4, '')
		FROM workflows w LEFT JOIN schedules s ON s.workflow_slug = w.slug
		WHERE w.project_id = $1 AND w.slug = $2
		FOR UPDATE OF w`, projeto, w.Slug, def, w.Schedule).Scan(&exists, &graphChanged, &scheduleChanged)
	if errors.Is(err, pgx.ErrNoRows) {
		scheduleChanged = w.Schedule != ""
	} else if err != nil {
		return err
	}
	if !exists || graphChanged || scheduleChanged {
		var project string
		if err := tx.QueryRow(ctx, `SELECT slug FROM projects WHERE id = $1`, projeto).Scan(&project); err != nil {
			return err
		}
		if err := recordEvent(ctx, tx, events.WorkflowPublished, w.Slug, actor, map[string]any{
			"project": project, "new": !exists, "schedule_changed": scheduleChanged,
		}); err != nil {
			return err
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO workflows (id, project_id, slug, name, definicao)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, slug) DO UPDATE
		SET name = EXCLUDED.name, definicao = EXCLUDED.definicao, updated_at = now()`,
		uuid.New(), projeto, w.Slug, w.Name, def)
	if err != nil {
		return fmt.Errorf("publicando workflow %q: %w", w.Slug, err)
	}

	if w.Schedule == "" {
		// No cron: it removes the schedule if there was one. Taking `schedule`
		// out of the YAML has to unschedule, not leave the old schedule alive.
		if _, err := tx.Exec(ctx, `DELETE FROM schedules WHERE workflow_slug = $1`, w.Slug); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	// `ultimo_slot` is NOT overwritten on update: republishing a workflow must
	// not
	// make the scheduler recreate slots that were already materialized.
	_, err = tx.Exec(ctx, `
		INSERT INTO schedules (id, workflow_slug, cron, timezone, catchup, ativo)
		VALUES ($1, $2, $3, $4, $5, true)
		ON CONFLICT (workflow_slug) DO UPDATE
		SET cron = EXCLUDED.cron, timezone = EXCLUDED.timezone,
		    catchup = EXCLUDED.catchup, atualizado_em = now()`,
		uuid.New(), w.Slug, w.Schedule, "UTC", false)
	if err != nil {
		return fmt.Errorf("publishing the schedule for %q: %w", w.Slug, err)
	}
	return tx.Commit(ctx)
}

// Definition le o grafo publicado.
func (r *WorkflowRepo) Definition(ctx context.Context, slug string) (wf.Workflow, error) {
	var bruto []byte
	if err := r.pool.QueryRow(ctx,
		`SELECT definicao FROM workflows WHERE slug = $1`, slug).Scan(&bruto); err != nil {
		return wf.Workflow{}, fmt.Errorf("workflow %q: %w", slug, err)
	}
	var w wf.Workflow
	return w, json.Unmarshal(bruto, &w)
}

// Definitions is every published workflow's current definition, by slug, in
// one query: what a page that shows many workflows reads instead of one
// Definition per row.
func (r *WorkflowRepo) Definitions(ctx context.Context) (map[string]wf.Workflow, error) {
	rows, err := r.pool.Query(ctx, `SELECT slug, definicao FROM workflows`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]wf.Workflow{}
	for rows.Next() {
		var slug string
		var bruto []byte
		if err := rows.Scan(&slug, &bruto); err != nil {
			return nil, err
		}
		var w wf.Workflow
		if err := json.Unmarshal(bruto, &w); err != nil {
			return nil, fmt.Errorf("workflow %q: %w", slug, err)
		}
		out[slug] = w
	}
	return out, rows.Err()
}

// PublishedGroupMembers returns the published workflows in a concurrency group
// that a publish of `publishing` into `projeto` leaves in place: their slug,
// group and limit, and nothing else.
//
// What `publish` sees is the files on its command line. The other members of
// their groups are here, and CheckGroups has to hold against them too, or
// publishing a folder one file at a time is a way around it (#32). Left out:
// the workflows this publish replaces, and with `prune` every other workflow
// of the project, which the prune removes. Other projects' stay members.
func (r *WorkflowRepo) PublishedGroupMembers(ctx context.Context, projeto uuid.UUID,
	publishing []string, prune bool) ([]wf.Workflow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT slug, definicao->>'Group', COALESCE((definicao->>'MaxActive')::int, 0)
		FROM workflows
		WHERE COALESCE(definicao->>'Group', '') <> ''
		  AND NOT (project_id = $1 AND ($3 OR slug = ANY($2)))
		ORDER BY slug`, projeto, publishing, prune)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []wf.Workflow
	for rows.Next() {
		var w wf.Workflow
		if err := rows.Scan(&w.Slug, &w.Group, &w.MaxActive); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Podar removes from the project the workflows that are NOT in the list, along
// with their
// agendas. Devolve os slugs removidos.
//
// It exists because publishing only ever added: taking a file out of the folder
// took nothing out of the database, and the scheduler went on materializing runs
// for a workflow nobody could see any more. With 15-minute schedules, that is
// invisible work running forever.
//
// The history (`runs`) is NOT deleted: it references the slug as text, not
// through a foreign key, precisely so it survives the definition's removal.
// Deleting the runs along with it would be deleting the evidence of what
// happened.
//
// Each removal is in the events feed, written by the same statement that
// deletes, with actor as who did it.
func (r *WorkflowRepo) Podar(ctx context.Context, projeto uuid.UUID, manter []string, actor string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		WITH gone AS (
		    DELETE FROM workflows
		    WHERE project_id = $1 AND NOT (slug = ANY($2))
		    RETURNING slug
		), recorded AS (
		    INSERT INTO events (kind, subject, actor, detail)
		    SELECT $3, slug, $4, jsonb_build_object('project', (SELECT slug FROM projects WHERE id = $1))
		    FROM gone
		)
		SELECT slug FROM gone`, projeto, manter, events.WorkflowRemoved, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var removed []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		removed = append(removed, slug)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(removed) == 0 {
		return nil, nil
	}

	// The schedule lives in a separate table, linked by slug as text — the
	// CASCADE does not reach it, and an orphaned schedule would go on creating
	// runs.
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM schedules WHERE workflow_slug = ANY($1)`, removed); err != nil {
		return removed, err
	}
	return removed, nil
}

// ScheduleRepo le e atualiza agendas.
type ScheduleRepo struct{ pool *Pool }

func NewScheduleRepo(p *Pool) *ScheduleRepo { return &ScheduleRepo{pool: p} }

// Ativas lists the schedules the scheduler has to evaluate.
func (r *ScheduleRepo) Active(ctx context.Context) ([]sch.Schedule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT workflow_slug, cron, timezone, catchup, ativo, ultimo_slot
		FROM schedules WHERE ativo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []sch.Schedule
	for rows.Next() {
		var s sch.Schedule
		if err := rows.Scan(&s.WorkflowSlug, &s.Cron, &s.Timezone,
			&s.Catchup, &s.Active, &s.LastSlot); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Of is a workflow's schedule, active or paused; false when it has none.
func (r *ScheduleRepo) Of(ctx context.Context, slug string) (sch.Schedule, bool, error) {
	var s sch.Schedule
	err := r.pool.QueryRow(ctx, `
		SELECT workflow_slug, cron, timezone, catchup, ativo, ultimo_slot
		FROM schedules WHERE workflow_slug = $1`, slug).
		Scan(&s.WorkflowSlug, &s.Cron, &s.Timezone, &s.Catchup, &s.Active, &s.LastSlot)
	if errors.Is(err, pgx.ErrNoRows) {
		return sch.Schedule{}, false, nil
	}
	return s, err == nil, err
}

// AdvanceSlot records how far the schedule has been materialized.
//
// The condition `ultimo_slot IS NULL OR ultimo_slot < $2` makes the operation
// idempotent and safe under concurrency: two schedulers evaluating the same
// schedule never make the marker go backwards.
// SetActive pauses or resumes a schedule and returns the resulting state.
//
// It returns rather than only writing because the UI toggles without knowing the
// current value: without the return, the screen would need a second query and
// would be open to a race between two operators clicking at the same time.
//
// Pausing does NOT cancel what is already queued: materialized runs are accepted
// work, and discarding them on a pause would surprise somebody who only wanted
// to stop creating new ones.
//
// actor is who did it, for the events feed: the console's operator, or "cli".
func (r *ScheduleRepo) SetActive(ctx context.Context, slug string, active1 bool, actor string) (bool, error) {
	return r.flip(ctx, "$3", "s.ativo IS DISTINCT FROM $3", slug, actor, active1)
}

// Toggle flips the current state in a single round trip to the database.
func (r *ScheduleRepo) Toggle(ctx context.Context, slug, actor string) (bool, error) {
	return r.flip(ctx, "NOT s.ativo", "true", slug, actor)
}

// flip sets the schedule's state to `to` when `changes` holds -- both SQL
// over the current row -- and records the change in the events feed IN THE
// SAME STATEMENT: a pause without its event, or the reverse, would be the
// feed lying.
//
// The UPDATE decides whether anything changed, not a read before it: under
// two operators clicking at once, Postgres re-checks an UPDATE's condition
// against the row the other one left, where a read in the same statement
// would still see the old one. When nothing changes, nothing is recorded and
// the current state is returned.
func (r *ScheduleRepo) flip(ctx context.Context, to, changes, slug, actor string, args ...any) (bool, error) {
	var on bool
	err := r.pool.QueryRow(ctx, `
		WITH after AS (
		    UPDATE schedules s SET ativo = `+to+`, atualizado_em = now()
		    WHERE s.workflow_slug = $1 AND `+changes+`
		    RETURNING s.ativo
		), recorded AS (
		    INSERT INTO events (kind, subject, actor)
		    SELECT CASE WHEN ativo THEN '`+string(events.ScheduleResumed)+`'
		                ELSE '`+string(events.SchedulePaused)+`' END, $1, $2
		    FROM after
		)
		SELECT ativo FROM after
		UNION ALL
		SELECT ativo FROM schedules WHERE workflow_slug = $1 AND NOT EXISTS (SELECT 1 FROM after)`,
		append([]any{slug, actor}, args...)...).Scan(&on)
	return on, err
}

func (r *ScheduleRepo) AdvanceSlot(ctx context.Context, slug string, slot time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE schedules
		SET ultimo_slot = $2, atualizado_em = now()
		WHERE workflow_slug = $1 AND (ultimo_slot IS NULL OR ultimo_slot < $2)`,
		slug, slot)
	return err
}
