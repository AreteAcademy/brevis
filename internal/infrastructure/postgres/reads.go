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

	// Params are the values the run was given. Read only by WorkflowHistory:
	// the lists that do not show them do not pay for them.
	Params map[string]string
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
	// Retried are the runs on their second attempt or later: a failure that
	// healed, which does not show as a failure (screen-overview-v2).
	Retried int
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

// RecentRun is one square of a workflow's recent-runs strip.
type RecentRun struct {
	ID     string
	Status string
	At     time.Time
}

// recentStatesSQL is RecentStates' query, named so a test can EXPLAIN the very
// text the console runs.
//
// One index scan per workflow, stopping after n (#109). It was a window --
// row_number() over every run of every workflow asked, then rn <= n -- which
// reads and sorts ALL of them: 100k runs over 50 workflows took ~95ms even with
// the index, 4.9MB sorted on disk, on every load and keystroke of the
// Workflows page. A LATERAL with a LIMIT reads n rows a workflow: ~2.4ms.
//
// DISTINCT because ANY($1) ignored a slug asked twice and unnest would not.
// The tie-break -- id DESC within an instant, id ascending in the answer -- is
// the window's, unchanged.
const recentStatesSQL = `
		SELECT s.slug, r.id::text, r.status, r.criado_em
		FROM (SELECT DISTINCT unnest($1::text[]) AS slug) s
		CROSS JOIN LATERAL (
		    SELECT id, status, criado_em FROM runs
		    WHERE workflow_slug = s.slug
		    ORDER BY criado_em DESC, id DESC
		    LIMIT $2
		) r
		ORDER BY s.slug, r.criado_em, r.id`

// RecentStates is the last n runs of each workflow asked, oldest to newest so
// a strip reads left to right like time. ONE query for a page rather than one
// per row; a workflow that never ran is absent from the map.
func (r *ReadRepo) RecentStates(ctx context.Context, slugs []string, n int) (map[string][]RecentRun, error) {
	out := map[string][]RecentRun{}
	if len(slugs) == 0 || n <= 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, recentStatesSQL, slugs, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var rr RecentRun
		if err := rows.Scan(&slug, &rr.ID, &rr.Status, &rr.At); err != nil {
			return nil, err
		}
		out[slug] = append(out[slug], rr)
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
func (r *ReadRepo) QueueDepth(ctx context.Context) (pending, claimed int, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE reivindicado_em IS NULL),
		       count(*) FILTER (WHERE reivindicado_em IS NOT NULL)
		FROM queue_items`).Scan(&pending, &claimed)
	return
}

// Indicators aggregates the recent window for the four cards at the top.
//
// One query, with FILTER, rather than four: those would be four scans of the
// same table over the same time predicate.
func (r *ReadRepo) Indicators(ctx context.Context, window time.Duration) (Indicators, error) {
	return r.IndicatorsFor(ctx, window, "")
}

// IndicatorsFor is Indicators scoped to ONE workflow, and the same query.
//
// An empty slug means every workflow, which is what the dashboard asks for.
// Writing the aggregation twice -- once global, once per workflow -- would be
// two places for "what counts as finished", and the answer has already moved
// once: `Ratio` excludes what is still running, and a second copy would not
// know that.
func (r *ReadRepo) IndicatorsFor(ctx context.Context, window time.Duration, workflow string) (Indicators, error) {
	var i Indicators
	var meanMs *float64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'success'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status IN ('running', 'retrying')),
		       count(*) FILTER (WHERE status = 'queued'),
		       avg(EXTRACT(EPOCH FROM (terminado_em - iniciado_em)) * 1000)
		         FILTER (WHERE terminado_em IS NOT NULL AND iniciado_em IS NOT NULL),
		       count(*) FILTER (WHERE attempt > 0)
		FROM runs
		WHERE criado_em >= now() - $1::interval
		  AND ($2 = '' OR workflow_slug = $2)`, window, workflow).
		Scan(&i.Total, &i.Succeeded, &i.Failed, &i.Running, &i.Pending, &meanMs, &i.Retried)
	if err != nil {
		return i, err
	}
	if meanMs != nil {
		i.MeanDuration = time.Duration(*meanMs) * time.Millisecond
	}
	return i, nil
}

// Day is one square of the calendar heatmap.
type Day struct {
	Date      time.Time
	Total     int
	Succeeded int
	Failed    int
	Open      int // running, retrying or queued
}

// RunsPerDay returns one row per DAY that had a run, for one workflow.
//
// Unlike RunsPerHour it does NOT fill the gaps with a generate_series, and the
// difference is the shape of the two charts. A bar chart with a missing hour
// compresses time and invents continuity; a calendar has a square for every day
// whether or not anything happened, so the empty ones are drawn by the renderer
// from the date range and never travel over the wire. Sending 365 mostly-zero
// rows to draw nothing would be the same picture at ten times the cost.
func (r *ReadRepo) RunsPerDay(ctx context.Context, workflow string, days int) ([]Day, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT (criado_em AT TIME ZONE 'UTC')::date AS dia,
		       count(*),
		       count(*) FILTER (WHERE status = 'success'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status IN ('running', 'retrying', 'queued', 'pending'))
		FROM runs
		WHERE workflow_slug = $1
		  AND criado_em >= (now() AT TIME ZONE 'UTC')::date - ($2::int - 1)
		GROUP BY 1
		ORDER BY 1`, workflow, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Day
	for rows.Next() {
		var d Day
		if err := rows.Scan(&d.Date, &d.Total, &d.Succeeded, &d.Failed, &d.Open); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Grain is a chart's bucket: an hour or a day. A closed type, mapped to the
// SQL literal here -- a value from a URL never reaches the query.
type Grain int

const (
	ByHour Grain = iota
	ByDay
)

func (g Grain) unit() string {
	if g == ByDay {
		return "day"
	}
	return "hour"
}

// RunsPer returns one column per hour or per day, the empty ones INCLUDED.
//
// The left `generate_series` is the point: without it an hour with no run
// simply would not appear, and the chart would compress time, giving the
// impression of continuous activity where there was a gap.
func (r *ReadRepo) RunsPer(ctx context.Context, g Grain, n int) ([]Bucket, error) {
	unit := g.unit()
	rows, err := r.pool.Query(ctx, `
		WITH janela AS (
		    SELECT generate_series(
		        date_trunc('`+unit+`', now()) - ($1::int - 1) * interval '1 `+unit+`',
		        date_trunc('`+unit+`', now()),
		        interval '1 `+unit+`') AS inicio
		)
		SELECT j.inicio,
		       count(r.id) FILTER (WHERE r.status = 'success'),
		       count(r.id) FILTER (WHERE r.status = 'failed'),
		       count(r.id) FILTER (WHERE r.status IN ('running', 'retrying')),
		       count(r.id) FILTER (WHERE r.status = 'queued'),
		       avg(EXTRACT(EPOCH FROM (r.terminado_em - r.iniciado_em)) * 1000)
		FROM janela j
		LEFT JOIN runs r
		       ON date_trunc('`+unit+`', r.criado_em) = j.inicio
		GROUP BY j.inicio
		ORDER BY j.inicio`, n)
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

// RunFilter is the run screen's query. Empty fields do not filter.
type RunFilter struct {
	State string
	// States is several at once -- "in flight" is three -- and is ORed with
	// nothing: State and States are not meant together.
	States []string
	// Workflow is a piece of the slug, matched without case. The whole slug
	// other screens link with is a piece of itself, so their links still land.
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
	put := func(sql string, value any) {
		args = append(args, value)
		cond = append(cond, fmt.Sprintf(sql, len(args)))
	}
	if f.State != "" {
		put("status = $%d", f.State)
	}
	if len(f.States) > 0 {
		put("status = ANY($%d)", f.States)
	}
	if f.Workflow != "" {
		put(`workflow_slug ILIKE $%d ESCAPE '\'`, "%"+likeLiteral(f.Workflow)+"%")
	}
	if f.De != nil {
		put("criado_em >= $%d", *f.De)
	}
	if f.Ate != nil {
		put("criado_em < $%d", *f.Ate)
	}
	return strings.Join(cond, " AND "), args
}

// likeLiteral makes typed text match itself in a LIKE: a `%` or `_` in the
// search box is a character somebody is looking for, not a wildcard.
func likeLiteral(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
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

// CountRunsByState counts the filter's runs per state, IGNORING its own state:
// the Runs list's state filter says, beside each choice, what its link would
// show with the rest of the filter as it is. One GROUP BY over the same
// predicate the list uses.
func (r *ReadRepo) CountRunsByState(ctx context.Context, f RunFilter) (map[string]int, error) {
	f.State = ""
	predicado, args := f.where()
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf(`SELECT status, count(*) FROM runs WHERE %s GROUP BY status`, predicado), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// LoadDay is one day of what a workflow's pipelines loaded.
//
// Sums for the volumes and a MEAN for the durations, because the two answer
// different questions: "how much did this workflow move that day" is a total,
// and "is a run getting slower" is per run. A summed duration would climb
// whenever the schedule got denser and say nothing about the pipeline.
type LoadDay struct {
	Date      time.Time
	Runs      int
	Rows      int64
	Records   int64
	Ignored   int64
	BytesIn   int64
	BytesOut  int64
	ExtractMs int64 // mean over the day
	LoadMs    int64 // mean over the day
}

// BytesPerRow is the number the note actually asked for: a load whose size
// grows while its row count holds flat is a schema that gained a column.
//
// Zero rows returns zero rather than dividing: a day with no rows has no answer
// to this, and an infinity on a chart is worse than a gap.
func (d LoadDay) BytesPerRow() int64 {
	if d.Rows == 0 {
		return 0
	}
	return d.BytesOut / d.Rows
}

// RowsPerSecond over the load phase. Zero when nothing was loaded or when the
// load was too fast to have measured a millisecond.
func (d LoadDay) RowsPerSecond() int64 {
	if d.LoadMs == 0 {
		return 0
	}
	return d.Rows * 1000 / d.LoadMs
}

// LoadTrend returns one row per day a workflow loaded anything, oldest first.
//
// Days with NO load are absent rather than zero, and that is deliberate in a
// way the calendar heatmap is not. The heatmap answers "did it run", so a blank
// day is information. This answers "is it getting slower", and a zero would not
// be a slow day -- it would be a day that is not in the series at all, dragging
// every average through a floor that never happened. The screen draws the gap.
func (r *ReadRepo) LoadTrend(ctx context.Context, workflow string, days int) ([]LoadDay, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT (em AT TIME ZONE 'UTC')::date AS dia,
		       count(*),
		       sum(linhas), sum(registros), sum(ignorados),
		       sum(bytes_entrada), sum(bytes_saida),
		       avg(extract_ms)::bigint, avg(load_ms)::bigint
		FROM load_metrics
		WHERE workflow_slug = $1
		  AND em >= (now() AT TIME ZONE 'UTC')::date - ($2::int - 1)
		GROUP BY 1
		ORDER BY 1`, workflow, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LoadDay
	for rows.Next() {
		var d LoadDay
		if err := rows.Scan(&d.Date, &d.Runs, &d.Rows, &d.Records, &d.Ignored,
			&d.BytesIn, &d.BytesOut, &d.ExtractMs, &d.LoadMs); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Connects is, per workflow, the scheme of every target its runs landed since
// `since` -- `bigquery://acme/bronze/orders` is `bigquery` -- once each, in
// alphabetical order: what the Workflows list draws as what a workflow talks
// to. A target with no scheme, a legacy table name, says nothing about where
// it lives and is left out. The schemes are as written; naming their logos is
// the screen's.
func (r *ReadRepo) Connects(ctx context.Context, since time.Time) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT workflow_slug, split_part(target, '://', 1)
		FROM landings
		WHERE loaded_at >= $1 AND strpos(target, '://') > 1
		ORDER BY 1, 2`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var slug, scheme string
		if err := rows.Scan(&slug, &scheme); err != nil {
			return nil, err
		}
		out[slug] = append(out[slug], scheme)
	}
	return out, rows.Err()
}

// WorkflowHistory is a workflow's runs, newest first, with the values each
// was given: the workflow page's Runs tab. With a day, only that day's -- a
// UTC day by when the run was created, the same day the calendar counts it
// in.
func (r *ReadRepo) WorkflowHistory(ctx context.Context, slug string, day *time.Time, limit int) ([]RunSummary, error) {
	var from, to *time.Time
	if day != nil {
		f := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		t := f.Add(24 * time.Hour)
		from, to = &f, &t
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro, params
		FROM runs
		WHERE workflow_slug = $1
		  AND ($2::timestamptz IS NULL OR criado_em >= $2)
		  AND ($3::timestamptz IS NULL OR criado_em < $3)
		ORDER BY criado_em DESC
		LIMIT $4`, slug, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RunSummary
	for rows.Next() {
		var r RunSummary
		var ini, end *time.Time
		if err := rows.Scan(&r.ID, &r.WorkflowSlug, &r.Status, &r.TriggerType, &r.Attempt,
			&r.LogicalDate, &r.CreatedAt, &ini, &end, &r.Err, &r.Params); err != nil {
			return nil, err
		}
		r.StartedAt = ini
		if ini != nil && end != nil {
			d := end.Sub(*ini)
			r.Duration = &d
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StepRun is how a step went the last time it ran.
type StepRun struct {
	Status     string
	DurationMs int64
}

// LastStepRuns is, for each step of a workflow, its last attempt in the
// newest run that ran it: a step the last run skipped keeps what it did
// before. The workflow page's graph draws it on each card, and a mapped
// step's instances are one row each -- the newest is the one shown.
func (r *ReadRepo) LastStepRuns(ctx context.Context, slug string) (map[string]StepRun, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (t.node_id) t.node_id, t.status,
		       COALESCE(EXTRACT(EPOCH FROM (t.terminado_em - t.iniciado_em)) * 1000, 0)::bigint
		FROM task_runs t JOIN runs r ON r.id = t.run_id
		WHERE r.workflow_slug = $1
		ORDER BY t.node_id, r.criado_em DESC, t.attempt DESC, t.iniciado_em DESC NULLS LAST`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]StepRun{}
	for rows.Next() {
		var node string
		var s StepRun
		if err := rows.Scan(&node, &s.Status, &s.DurationMs); err != nil {
			return nil, err
		}
		out[node] = s
	}
	return out, rows.Err()
}

// StepConnects is Connects by step: the scheme of every target each step of
// a workflow landed since `since`, once each, in order.
func (r *ReadRepo) StepConnects(ctx context.Context, slug string, since time.Time) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT node_id, split_part(target, '://', 1)
		FROM landings
		WHERE workflow_slug = $1 AND loaded_at >= $2 AND strpos(target, '://') > 1
		ORDER BY 1, 2`, slug, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var node, scheme string
		if err := rows.Scan(&node, &scheme); err != nil {
			return nil, err
		}
		out[node] = append(out[node], scheme)
	}
	return out, rows.Err()
}
