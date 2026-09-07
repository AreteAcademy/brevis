package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file holds the READ queries the UI makes.
//
// Kept apart from the write repositories on purpose: the UI needs flattened
// projections and aggregates, which do not correspond to the domain's entities.
// Mixing the two would make the domain carry fields that exist only for the
// screen.

// RunSummary is one row of the run list.
type RunSummary struct {
	ID           string
	WorkflowSlug string
	Status       string
	TriggerType  string
	Attempt      int
	LogicalDate  *time.Time
	CreatedAt    time.Time
	StartedAt    *time.Time
	Duration     *time.Duration
	Err          string
}

// WorkflowSummary joins the workflow, its schedule and the last run's state.
type WorkflowSummary struct {
	Slug        string
	Name        string
	Project     string
	Cron        string
	Timezone    string
	Catchup     bool
	Active      bool
	HasSchedule bool
	LastSlot    *time.Time
	LastStatus  string
	TotalRuns   int

	// From the last run — the list's "Latest Run" column.
	LastRunID *string
	LastRunAt *time.Time

	Tags []string

	// ProximaRun does not come from the database: it is computed from the cron,
	// in the consumer. Storing it would demand a recompute on every schedule
	// change and living with a stale value in between.
	NextRun *time.Time
}

// Indicators is the Overview's header.
type Indicators struct {
	Total        int
	Succeeded    int
	Failed       int
	Running      int
	Pending      int
	MeanDuration time.Duration
}

// Ratio returns `parte` as a percentage of everything already finished.
//
// The denominator excludes what is still running: counting an in-flight run as
// a "non-success" makes the rate plunge during a burst of work and climb back on
// its own afterwards, with nothing having changed.
func (i Indicators) Ratio(part int) float64 {
	finished := i.Succeeded + i.Failed
	if finished == 0 {
		return 0
	}
	return float64(part) * 100 / float64(finished)
}

// Bucket is one column of the run chart.
type Bucket struct {
	Start        time.Time
	Succeeded    int
	Failed       int
	Running      int
	Queued       int
	MeanDuration time.Duration
}

// Total sums the whole bucket — the column's height.
func (b Bucket) Total() int { return b.Succeeded + b.Failed + b.Running + b.Queued }

// ScheduleSummary is the minimum needed to compute the next trigger.
type ScheduleSummary struct {
	WorkflowSlug string
	Cron         string
	Timezone     string
	Active       bool
}

// ProjectSummary counts what exists under a project.
type ProjectSummary struct {
	Slug      string
	Name      string
	Workflows int
	Runs      int
	CreatedAt time.Time
}

// ReadRepo serves the UI.
type ReadRepo struct{ pool *Pool }

func NewReadRepo(p *Pool) *ReadRepo { return &ReadRepo{pool: p} }

// CountByStatus feeds the dashboard's cards.
func (r *ReadRepo) CountByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT status, count(*) FROM runs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// LatestRuns lists the most recent runs.
func (r *ReadRepo) LatestRuns(ctx context.Context, limite int) ([]RunSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		ORDER BY criado_em DESC
		LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return varrerRuns(rows)
}

// varrerRuns reads the columns LatestRuns and InFlight select, in the same
// order. Two identical scans would diverge on the first new column.
func varrerRuns(rows pgx.Rows) ([]RunSummary, error) {
	var out []RunSummary
	for rows.Next() {
		var r RunSummary
		var ini, end *time.Time
		if err := rows.Scan(&r.ID, &r.WorkflowSlug, &r.Status, &r.TriggerType, &r.Attempt,
			&r.LogicalDate, &r.CreatedAt, &ini, &end, &r.Err); err != nil {
			return nil, err
		}
		r.StartedAt = ini
		// Duration only exists when the run actually started AND finished;
		// computing it with either side null would produce a meaningless
		// number.
		if ini != nil && end != nil {
			d := end.Sub(*ini)
			r.Duration = &d
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Workflows lists the published workflows with their schedule and last state.
func (r *ReadRepo) Workflows(ctx context.Context) ([]WorkflowSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT w.slug, w.name, p.slug,
		       COALESCE(s.cron, ''), COALESCE(s.timezone, ''),
		       COALESCE(s.catchup, false), COALESCE(s.ativo, false),
		       s.workflow_slug IS NOT NULL, s.ultimo_slot,
		       COALESCE(u.status, ''), u.id::text, u.criado_em,
		       (SELECT count(*) FROM runs WHERE workflow_slug = w.slug),
		       -- The Tags key may not exist (a workflow published before the
		       -- field) or arrive as JSON null (no tags). In both cases the
		       -- expansion blows up with "cannot extract elements from a scalar"
		       -- and takes the whole list down over ONE row. The CASE normalizes
		       -- it to an empty array before expanding.
		       COALESCE((
		           SELECT array_agg(t) FROM jsonb_array_elements_text(
		               CASE WHEN jsonb_typeof(w.definicao->'Tags') = 'array'
		                    THEN w.definicao->'Tags' ELSE '[]'::jsonb END) t
		       ), '{}')
		FROM workflows w
		JOIN projects p ON p.id = w.project_id
		LEFT JOIN schedules s ON s.workflow_slug = w.slug
		-- LATERAL rather than a subselect per column: this way the id, the status
		-- and the instant of the last run come from the SAME row. Three
		-- independent subselects could mix different runs.
		LEFT JOIN LATERAL (
		    SELECT id, status, criado_em FROM runs
		    WHERE workflow_slug = w.slug ORDER BY criado_em DESC LIMIT 1
		) u ON true
		ORDER BY w.slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkflowSummary
	for rows.Next() {
		var w WorkflowSummary
		if err := rows.Scan(&w.Slug, &w.Name, &w.Project, &w.Cron, &w.Timezone,
			&w.Catchup, &w.Active, &w.HasSchedule, &w.LastSlot, &w.LastStatus,
			&w.LastRunID, &w.LastRunAt, &w.TotalRuns, &w.Tags); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Projects lists the projects with their totals.
func (r *ReadRepo) Projects(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.slug, p.name, p.created_at,
		       (SELECT count(*) FROM workflows w WHERE w.project_id = p.id),
		       (SELECT count(*) FROM runs r
		         JOIN workflows w2 ON w2.slug = r.workflow_slug AND w2.project_id = p.id)
		FROM projects p
		ORDER BY p.slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProjectSummary
	for rows.Next() {
		var p ProjectSummary
		if err := rows.Scan(&p.Slug, &p.Name, &p.CreatedAt, &p.Workflows, &p.Runs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// QueueDepth shows the queue on the dashboard.
func (r *ReadRepo) QueueDepth(ctx context.Context) (pendentes, claimed int, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE reivindicado_em IS NULL),
		       count(*) FILTER (WHERE reivindicado_em IS NOT NULL)
		FROM queue_items`).Scan(&pendentes, &claimed)
	return
}

// Indicators aggregates the recent window for the four cards at the top.
//
// One query, with FILTER, rather than four: those would be four scans of the
// same table over the same time predicate.
func (r *ReadRepo) Indicators(ctx context.Context, window time.Duration) (Indicators, error) {
	var i Indicators
	var meanMs *float64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'success'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status IN ('running', 'retrying')),
		       count(*) FILTER (WHERE status = 'queued'),
		       avg(EXTRACT(EPOCH FROM (terminado_em - iniciado_em)) * 1000)
		         FILTER (WHERE terminado_em IS NOT NULL AND iniciado_em IS NOT NULL)
		FROM runs
		WHERE criado_em >= now() - $1::interval`, window).
		Scan(&i.Total, &i.Succeeded, &i.Failed, &i.Running, &i.Pending, &meanMs)
	if err != nil {
		return i, err
	}
	if meanMs != nil {
		i.MeanDuration = time.Duration(*meanMs) * time.Millisecond
	}
	return i, nil
}

// RunsPerHour returns one column per hour, the empty ones INCLUDED.
//
// The left `generate_series` is the point: without it an hour with no run
// simply would not appear, and the chart would compress time, giving the
// impression of continuous activity where there was a gap.
func (r *ReadRepo) RunsPerHour(ctx context.Context, horas int) ([]Bucket, error) {
	rows, err := r.pool.Query(ctx, `
		WITH janela AS (
		    SELECT generate_series(
		        date_trunc('hour', now()) - make_interval(hours => $1 - 1),
		        date_trunc('hour', now()),
		        interval '1 hour') AS inicio
		)
		SELECT j.inicio,
		       count(r.id) FILTER (WHERE r.status = 'success'),
		       count(r.id) FILTER (WHERE r.status = 'failed'),
		       count(r.id) FILTER (WHERE r.status IN ('running', 'retrying')),
		       count(r.id) FILTER (WHERE r.status = 'queued'),
		       avg(EXTRACT(EPOCH FROM (r.terminado_em - r.iniciado_em)) * 1000)
		FROM janela j
		LEFT JOIN runs r
		       ON date_trunc('hour', r.criado_em) = j.inicio
		GROUP BY j.inicio
		ORDER BY j.inicio`, horas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Bucket
	for rows.Next() {
		var b Bucket
		var meanMs *float64
		if err := rows.Scan(&b.Start, &b.Succeeded, &b.Failed, &b.Running,
			&b.Queued, &meanMs); err != nil {
			return nil, err
		}
		if meanMs != nil {
			b.MeanDuration = time.Duration(*meanMs) * time.Millisecond
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// InFlight lists what is running or waiting its turn, oldest first.
//
// The order is ascending on purpose: whatever has been in the queue longest is
// what deserves attention, and sorting by most recent would hide exactly that.
func (r *ReadRepo) InFlight(ctx context.Context, limite int) ([]RunSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE status IN ('queued', 'running', 'retrying')
		ORDER BY criado_em
		LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return varrerRuns(rows)
}

// Schedules returns every schedule, active or not. The DAG list shows the paused
// ones too — hiding them from the screen would hide the reason nothing runs.
func (r *ReadRepo) Schedules(ctx context.Context) ([]ScheduleSummary, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT workflow_slug, cron, timezone, ativo FROM schedules ORDER BY workflow_slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScheduleSummary
	for rows.Next() {
		var a ScheduleSummary
		if err := rows.Scan(&a.WorkflowSlug, &a.Cron, &a.Timezone, &a.Active); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// WorkflowRuns lists the runs of a single workflow, for its own screen.
func (r *ReadRepo) WorkflowRuns(ctx context.Context, slug string, limite int) ([]RunSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE workflow_slug = $1
		ORDER BY criado_em DESC
		LIMIT $2`, slug, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return varrerRuns(rows)
}

// RunFilter is the run screen's query. Empty fields do not filter.
type RunFilter struct {
	State    string
	Workflow string
	De       *time.Time
	Ate      *time.Time
	Limite   int
	Offset   int
}

// where builds the predicate and the arguments together, so one never drifts
// out of sync with the other — the most common way to get dynamic SQL wrong.
func (f RunFilter) where() (string, []any) {
	cond := []string{"true"}
	var args []any
	poe := func(sql string, value any) {
		args = append(args, value)
		cond = append(cond, fmt.Sprintf(sql, len(args)))
	}
	if f.State != "" {
		poe("status = $%d", f.State)
	}
	if f.Workflow != "" {
		poe("workflow_slug = $%d", f.Workflow)
	}
	if f.De != nil {
		poe("criado_em >= $%d", *f.De)
	}
	if f.Ate != nil {
		poe("criado_em < $%d", *f.Ate)
	}
	return strings.Join(cond, " AND "), args
}

// Runs lists runs with filtering and pagination.
func (r *ReadRepo) Runs(ctx context.Context, f RunFilter) ([]RunSummary, error) {
	if f.Limite <= 0 {
		f.Limite = 50
	}
	predicado, args := f.where()
	args = append(args, f.Limite, f.Offset)

	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE %s
		ORDER BY criado_em DESC
		LIMIT $%d OFFSET $%d`, predicado, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return varrerRuns(rows)
}

// CountRuns returns the total for the SAME filter, so the pagination knows how
// many pages there are.
func (r *ReadRepo) CountRuns(ctx context.Context, f RunFilter) (int, error) {
	predicado, args := f.where()
	var n int
	err := r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM runs WHERE %s`, predicado), args...).Scan(&n)
	return n, err
}
