package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
)

// streakSQL counts a workflow's failed runs since its last success. Only
// failed and success are read: a cancel says nothing about whether the
// workflow works, and a run still going has not finished. $1 is the slug.
const streakSQL = `
		SELECT count(*) FROM runs f
		WHERE f.workflow_slug = $1 AND f.status = 'failed'
		  AND f.criado_em > COALESCE((
		      SELECT max(s.criado_em) FROM runs s
		      WHERE s.workflow_slug = $1 AND s.status = 'success'), '-infinity')`

// FailureStreak is how many times in a row the workflow failed, counted back
// from its newest finished run to its last success; 0 when the last one
// passed or it never ran.
func (r *RunRepo) FailureStreak(ctx context.Context, slug string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, streakSQL, slug).Scan(&n)
	return n, err
}

// FailingWorkflow is a workflow whose last finished run failed.
type FailingWorkflow struct {
	Slug     string
	RunID    uuid.UUID
	FailedAt time.Time
	Streak   int
}

// failingWorkflowsSQL reads, per declared workflow, its last finished run
// and, when that failed, the streak -- each through the
// (workflow_slug, criado_em DESC) index. A workflow is declared while it is in
// a project, paused or not: pausing a broken workflow does not fix it, and one
// taken out of the project leaves its runs as history but is not failing.
const failingWorkflowsSQL = `
		SELECT w.slug, l.id, COALESCE(l.terminado_em, l.criado_em), s.n
		FROM (SELECT DISTINCT slug FROM workflows) w
		CROSS JOIN LATERAL (
		    SELECT id, status, criado_em, terminado_em FROM runs
		    WHERE workflow_slug = w.slug AND status IN ('success', 'failed')
		    ORDER BY criado_em DESC, id DESC
		    LIMIT 1
		) l
		CROSS JOIN LATERAL (
		    SELECT count(*) AS n FROM runs f
		    WHERE f.workflow_slug = w.slug AND f.status = 'failed'
		      AND f.criado_em > COALESCE((
		          SELECT max(s.criado_em) FROM runs s
		          WHERE s.workflow_slug = w.slug AND s.status = 'success'), '-infinity')
		) s
		WHERE l.status = $1
		ORDER BY s.n DESC, l.criado_em DESC, w.slug`

// FailingWorkflows lists the workflows failing now, worst first: the longest
// streak, then the most recent failure. One query for the whole list.
func (r *RunRepo) FailingWorkflows(ctx context.Context) ([]FailingWorkflow, error) {
	rows, err := r.pool.Query(ctx, failingWorkflowsSQL, dom.StatusFailed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FailingWorkflow
	for rows.Next() {
		var f FailingWorkflow
		if err := rows.Scan(&f.Slug, &f.RunID, &f.FailedAt, &f.Streak); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
