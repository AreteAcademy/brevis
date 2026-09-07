package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

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
func (r *WorkflowRepo) Publicar(ctx context.Context, w wf.Workflow, projeto uuid.UUID) error {
	def, err := json.Marshal(w)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do commit

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
func (r *WorkflowRepo) Podar(ctx context.Context, projeto uuid.UUID, manter []string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		DELETE FROM workflows
		WHERE project_id = $1 AND NOT (slug = ANY($2))
		RETURNING slug`, projeto, manter)
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
func (r *ScheduleRepo) SetActive(ctx context.Context, slug string, active1 bool) (bool, error) {
	var resultado bool
	err := r.pool.QueryRow(ctx, `
		UPDATE schedules SET ativo = $2, atualizado_em = now()
		WHERE workflow_slug = $1
		RETURNING ativo`, slug, active1).Scan(&resultado)
	return resultado, err
}

// Alternar flips the current state in a single round trip to the database.
func (r *ScheduleRepo) Toggle(ctx context.Context, slug string) (bool, error) {
	var resultado bool
	err := r.pool.QueryRow(ctx, `
		UPDATE schedules SET ativo = NOT ativo, atualizado_em = now()
		WHERE workflow_slug = $1
		RETURNING ativo`, slug).Scan(&resultado)
	return resultado, err
}

func (r *ScheduleRepo) AdvanceSlot(ctx context.Context, slug string, slot time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE schedules
		SET ultimo_slot = $2, atualizado_em = now()
		WHERE workflow_slug = $1 AND (ultimo_slot IS NULL OR ultimo_slot < $2)`,
		slug, slot)
	return err
}
