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

// ResumoRun is one row of the run list.
type ResumoRun struct {
	ID           string
	WorkflowSlug string
	Status       string
	TriggerType  string
	Tentativa    int
	LogicalDate  *time.Time
	CriadoEm     time.Time
	IniciadoEm   *time.Time
	Duracao      *time.Duration
	Erro         string
}

// ResumoWorkflow joins the workflow, its schedule and the last run's state.
type ResumoWorkflow struct {
	Slug         string
	Nome         string
	Projeto      string
	Cron         string
	Timezone     string
	Catchup      bool
	Ativo        bool
	TemAgenda    bool
	UltimoSlot   *time.Time
	UltimoStatus string
	TotalRuns    int

	// From the last run — the list's "Latest Run" column.
	UltimaRunID *string
	UltimaRunEm *time.Time

	Tags []string

	// ProximaRun does not come from the database: it is computed from the cron,
	// in the consumer. Storing it would demand a recompute on every schedule
	// change and living with a stale value in between.
	ProximaRun *time.Time
}

// Indicadores is the Overview's header.
type Indicadores struct {
	Total        int
	Sucesso      int
	Falha        int
	EmExecucao   int
	Pendentes    int
	DuracaoMedia time.Duration
}

// Razao returns `parte` as a percentage of everything already finished.
//
// The denominator excludes what is still running: counting an in-flight run as
// a "non-success" makes the rate plunge during a burst of work and climb back on
// its own afterwards, with nothing having changed.
func (i Indicadores) Razao(parte int) float64 {
	concluidas := i.Sucesso + i.Falha
	if concluidas == 0 {
		return 0
	}
	return float64(parte) * 100 / float64(concluidas)
}

// Balde is one column of the run chart.
type Balde struct {
	Inicio       time.Time
	Sucesso      int
	Falha        int
	Executando   int
	Fila         int
	DuracaoMedia time.Duration
}

// Total sums the whole bucket — the column's height.
func (b Balde) Total() int { return b.Sucesso + b.Falha + b.Executando + b.Fila }

// AgendaResumo is the minimum needed to compute the next trigger.
type AgendaResumo struct {
	WorkflowSlug string
	Cron         string
	Timezone     string
	Ativo        bool
}

// ResumoProjeto counts what exists under a project.
type ResumoProjeto struct {
	Slug      string
	Nome      string
	Workflows int
	Runs      int
	CriadoEm  time.Time
}

// LeituraRepo serves the UI.
type LeituraRepo struct{ pool *Pool }

func NewLeituraRepo(p *Pool) *LeituraRepo { return &LeituraRepo{pool: p} }

// ContagemPorStatus feeds the dashboard's cards.
func (r *LeituraRepo) ContagemPorStatus(ctx context.Context) (map[string]int, error) {
	linhas, err := r.pool.Query(ctx, `SELECT status, count(*) FROM runs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	out := map[string]int{}
	for linhas.Next() {
		var s string
		var n int
		if err := linhas.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, linhas.Err()
}

// UltimasRuns lists the most recent runs.
func (r *LeituraRepo) UltimasRuns(ctx context.Context, limite int) ([]ResumoRun, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		ORDER BY criado_em DESC
		LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()
	return varrerRuns(linhas)
}

// varrerRuns reads the columns UltimasRuns and EmAndamento select, in the same
// order. Two identical scans would diverge on the first new column.
func varrerRuns(linhas pgx.Rows) ([]ResumoRun, error) {
	var out []ResumoRun
	for linhas.Next() {
		var r ResumoRun
		var ini, fim *time.Time
		if err := linhas.Scan(&r.ID, &r.WorkflowSlug, &r.Status, &r.TriggerType, &r.Tentativa,
			&r.LogicalDate, &r.CriadoEm, &ini, &fim, &r.Erro); err != nil {
			return nil, err
		}
		r.IniciadoEm = ini
		// Duracao only exists when the run actually started AND finished;
		// computing it with either side null would produce a meaningless
		// number.
		if ini != nil && fim != nil {
			d := fim.Sub(*ini)
			r.Duracao = &d
		}
		out = append(out, r)
	}
	return out, linhas.Err()
}

// Workflows lists the published workflows with their schedule and last state.
func (r *LeituraRepo) Workflows(ctx context.Context) ([]ResumoWorkflow, error) {
	linhas, err := r.pool.Query(ctx, `
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
	defer linhas.Close()

	var out []ResumoWorkflow
	for linhas.Next() {
		var w ResumoWorkflow
		if err := linhas.Scan(&w.Slug, &w.Nome, &w.Projeto, &w.Cron, &w.Timezone,
			&w.Catchup, &w.Ativo, &w.TemAgenda, &w.UltimoSlot, &w.UltimoStatus,
			&w.UltimaRunID, &w.UltimaRunEm, &w.TotalRuns, &w.Tags); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, linhas.Err()
}

// Projetos lists the projects with their totals.
func (r *LeituraRepo) Projetos(ctx context.Context) ([]ResumoProjeto, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT p.slug, p.name, p.created_at,
		       (SELECT count(*) FROM workflows w WHERE w.project_id = p.id),
		       (SELECT count(*) FROM runs r
		         JOIN workflows w2 ON w2.slug = r.workflow_slug AND w2.project_id = p.id)
		FROM projects p
		ORDER BY p.slug`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []ResumoProjeto
	for linhas.Next() {
		var p ResumoProjeto
		if err := linhas.Scan(&p.Slug, &p.Nome, &p.CriadoEm, &p.Workflows, &p.Runs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, linhas.Err()
}

// ProfundidadeDaFila shows the queue on the dashboard.
func (r *LeituraRepo) ProfundidadeDaFila(ctx context.Context) (pendentes, reivindicados int, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE reivindicado_em IS NULL),
		       count(*) FILTER (WHERE reivindicado_em IS NOT NULL)
		FROM queue_items`).Scan(&pendentes, &reivindicados)
	return
}

// Indicadores aggregates the recent window for the four cards at the top.
//
// One query, with FILTER, rather than four: those would be four scans of the
// same table over the same time predicate.
func (r *LeituraRepo) Indicadores(ctx context.Context, janela time.Duration) (Indicadores, error) {
	var i Indicadores
	var mediaMs *float64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'success'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status IN ('running', 'retrying')),
		       count(*) FILTER (WHERE status = 'queued'),
		       avg(EXTRACT(EPOCH FROM (terminado_em - iniciado_em)) * 1000)
		         FILTER (WHERE terminado_em IS NOT NULL AND iniciado_em IS NOT NULL)
		FROM runs
		WHERE criado_em >= now() - $1::interval`, janela).
		Scan(&i.Total, &i.Sucesso, &i.Falha, &i.EmExecucao, &i.Pendentes, &mediaMs)
	if err != nil {
		return i, err
	}
	if mediaMs != nil {
		i.DuracaoMedia = time.Duration(*mediaMs) * time.Millisecond
	}
	return i, nil
}

// ExecucoesPorHora returns one column per hour, the empty ones INCLUDED.
//
// The left `generate_series` is the point: without it an hour with no run
// simply would not appear, and the chart would compress time, giving the
// impression of continuous activity where there was a gap.
func (r *LeituraRepo) ExecucoesPorHora(ctx context.Context, horas int) ([]Balde, error) {
	linhas, err := r.pool.Query(ctx, `
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
	defer linhas.Close()

	var out []Balde
	for linhas.Next() {
		var b Balde
		var mediaMs *float64
		if err := linhas.Scan(&b.Inicio, &b.Sucesso, &b.Falha, &b.Executando,
			&b.Fila, &mediaMs); err != nil {
			return nil, err
		}
		if mediaMs != nil {
			b.DuracaoMedia = time.Duration(*mediaMs) * time.Millisecond
		}
		out = append(out, b)
	}
	return out, linhas.Err()
}

// EmAndamento lists what is running or waiting its turn, oldest first.
//
// The order is ascending on purpose: whatever has been in the queue longest is
// what deserves attention, and sorting by most recent would hide exactly that.
func (r *LeituraRepo) EmAndamento(ctx context.Context, limite int) ([]ResumoRun, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE status IN ('queued', 'running', 'retrying')
		ORDER BY criado_em
		LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()
	return varrerRuns(linhas)
}

// Agendas returns every schedule, active or not. The DAG list shows the paused
// ones too — hiding them from the screen would hide the reason nothing runs.
func (r *LeituraRepo) Agendas(ctx context.Context) ([]AgendaResumo, error) {
	linhas, err := r.pool.Query(ctx,
		`SELECT workflow_slug, cron, timezone, ativo FROM schedules ORDER BY workflow_slug`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []AgendaResumo
	for linhas.Next() {
		var a AgendaResumo
		if err := linhas.Scan(&a.WorkflowSlug, &a.Cron, &a.Timezone, &a.Ativo); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, linhas.Err()
}

// RunsDoWorkflow lists the runs of a single workflow, for its own screen.
func (r *LeituraRepo) RunsDoWorkflow(ctx context.Context, slug string, limite int) ([]ResumoRun, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE workflow_slug = $1
		ORDER BY criado_em DESC
		LIMIT $2`, slug, limite)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()
	return varrerRuns(linhas)
}

// FiltroRuns is the run screen's query. Empty fields do not filter.
type FiltroRuns struct {
	Estado   string
	Workflow string
	De       *time.Time
	Ate      *time.Time
	Limite   int
	Offset   int
}

// where builds the predicate and the arguments together, so one never drifts
// out of sync with the other — the most common way to get dynamic SQL wrong.
func (f FiltroRuns) where() (string, []any) {
	cond := []string{"true"}
	var args []any
	poe := func(sql string, valor any) {
		args = append(args, valor)
		cond = append(cond, fmt.Sprintf(sql, len(args)))
	}
	if f.Estado != "" {
		poe("status = $%d", f.Estado)
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
func (r *LeituraRepo) Runs(ctx context.Context, f FiltroRuns) ([]ResumoRun, error) {
	if f.Limite <= 0 {
		f.Limite = 50
	}
	predicado, args := f.where()
	args = append(args, f.Limite, f.Offset)

	linhas, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE %s
		ORDER BY criado_em DESC
		LIMIT $%d OFFSET $%d`, predicado, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()
	return varrerRuns(linhas)
}

// ContarRuns returns the total for the SAME filter, so the pagination knows how
// many pages there are.
func (r *LeituraRepo) ContarRuns(ctx context.Context, f FiltroRuns) (int, error) {
	predicado, args := f.where()
	var n int
	err := r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM runs WHERE %s`, predicado), args...).Scan(&n)
	return n, err
}
