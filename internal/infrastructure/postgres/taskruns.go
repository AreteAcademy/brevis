package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/google/uuid"

	exec "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
)

// This file closes a debt left open since PHASE 2: the `task_runs` table existed
// in the schema but was never populated, so retries were per Run and there was
// no per-step state. The DAG view needs exactly that.

// IniciarTask records a step's start.
//
// `ON CONFLICT DO UPDATE` on the (run, node, attempt) key: re-running the same
// step on the same attempt is idempotent, which matters when the dispatcher
// recovers an item from a dead worker and redoes it.
func (r *RunRepo) IniciarTask(ctx context.Context, runID uuid.UUID, step dom.StepKey, attempt int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, map_index, status, attempt, iniciado_em)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (run_id, node_id, attempt, map_index) DO UPDATE
		SET status = EXCLUDED.status, iniciado_em = now(), terminado_em = NULL, erro = ''`,
		uuid.New(), runID, step.Node, step.MapIndex, dom.StatusRunning, attempt)
	return err
}

// MarkSkipped records a step whose trigger rule was not satisfied.
//
// Not IniciarTask followed by TerminarTask, and the difference is the point:
// `iniciado_em` stays NULL. A skipped step never started, and stamping a start
// time would make it look like something that ran in zero seconds -- which is
// also what a step killed instantly looks like.
//
// `erro` carries WHY. A skipped step with no explanation sends whoever is
// looking at the graph to trace edges by hand, and that column is where the
// screen already shows a step's last word.
//
// ON CONFLICT because a run that retries re-evaluates every rule: attempt 0 of
// the second try overwrites attempt 0 of the first, and a step skipped once may
// well run the next time.
func (r *RunRepo) MarkSkipped(ctx context.Context, runID uuid.UUID, step dom.StepKey,
	attempt int, reason string) error {

	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, map_index, status, attempt, erro, terminado_em)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (run_id, node_id, attempt, map_index) DO UPDATE
		SET status = EXCLUDED.status, erro = EXCLUDED.erro,
		    iniciado_em = NULL, terminado_em = now(),
		    exit_code = NULL, log = ''`,
		uuid.New(), runID, step.Node, step.MapIndex, dom.StatusSkipped, attempt, reason)
	return err
}

// RecordStages records the advance of an SDK step's phases.
//
// It overwrites the whole array rather than appending: the runner's collector
// already keeps ONE entry per phase, with its current state, and the screen
// wants four boxes rather than a diary.
func (r *RunRepo) RecordStages(ctx context.Context, runID uuid.UUID, step dom.StepKey,
	attempt int, sdkVersion string, stages json.RawMessage) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE task_runs
		SET etapas = $5, sdk_versao = COALESCE(NULLIF($6, ''), sdk_versao)
		WHERE run_id = $1 AND node_id = $2 AND attempt = $3 AND map_index = $4`,
		runID, step.Node, attempt, step.MapIndex, stages, sdkVersion)
	return err
}

// RecordLoad keeps what one attempt of an SDK pipeline measured.
//
// UPSERT rather than INSERT, and that is a retry rather than a race: attempt 2
// of a step overwrites attempt 1's row. The trend asks "how much did this
// pipeline load that day", and a step that failed after loading 40,000 rows and
// then succeeded loading 48,000 loaded 48,000 -- counting both would invent
// 88,000 rows that never existed. The attempt is not in the key for exactly
// that reason; `task_runs` keeps the per-attempt history for whoever needs it.
func (r *RunRepo) RecordLoad(ctx context.Context, runID uuid.UUID, step dom.StepKey,
	workflow string, n exec.LoadNumbers) error {

	// `em` comes from the RUN and not from now(): a step that starts at 23:58
	// and ends at 00:04 belongs to the day of the run that asked for it, in the
	// same bucket as every other step of that run. Read from the row rather
	// than passed in, so the runner cannot hand over a different clock than the
	// one the calendar heatmap groups by.
	_, err := r.pool.Exec(ctx, `
		INSERT INTO load_metrics (
			run_id, node_id, map_index, workflow_slug, em,
			linhas, registros, ignorados, bytes_saida, load_ms,
			bytes_entrada, paginas, tentativas, extract_ms)
		SELECT $1, $2, $3, $4, r.criado_em,
		       $5, $6, $7, $8, $9, $10, $11, $12, $13
		FROM runs r WHERE r.id = $1
		ON CONFLICT (run_id, node_id, map_index) DO UPDATE SET
			linhas = EXCLUDED.linhas, registros = EXCLUDED.registros,
			ignorados = EXCLUDED.ignorados, bytes_saida = EXCLUDED.bytes_saida,
			load_ms = EXCLUDED.load_ms, bytes_entrada = EXCLUDED.bytes_entrada,
			paginas = EXCLUDED.paginas, tentativas = EXCLUDED.tentativas,
			extract_ms = EXCLUDED.extract_ms`,
		runID, step.Node, step.MapIndex, workflow,
		n.Rows, n.Records, n.Ignored, n.BytesOut, n.LoadMs,
		n.BytesIn, n.Pages, n.HTTPAttempts, n.ExtractMs)
	return err
}

// RecordContext stores what a step published, for the steps below it.
//
// Written the moment the step publishes rather than when it ends: a run that
// resumes reads this back, and the process that would write it later is exactly
// the one that may not survive.
func (r *RunRepo) RecordContext(ctx context.Context, runID uuid.UUID, step dom.StepKey,
	attempt int, published json.RawMessage) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE task_runs
		SET saida = $5
		WHERE run_id = $1 AND node_id = $2 AND attempt = $3 AND map_index = $4`,
		runID, step.Node, attempt, step.MapIndex, published)
	return err
}

// PublishedContext is what the steps of a run have published so far.
//
// DISTINCT ON keeps the latest attempt per step, which is the rule a retry
// needs: the previous attempt's output described work that did not finish, and
// the step below must not read it.
func (r *RunRepo) PublishedContext(ctx context.Context, runID uuid.UUID) (map[string]json.RawMessage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (node_id) node_id, saida
		FROM task_runs
		WHERE run_id = $1 AND saida IS NOT NULL
		ORDER BY node_id, attempt DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]json.RawMessage{}
	for rows.Next() {
		var nodeID string
		var raw []byte
		if err := rows.Scan(&nodeID, &raw); err != nil {
			return nil, err
		}
		out[nodeID] = json.RawMessage(raw)
	}
	return out, rows.Err()
}

// TerminarTask records the outcome.
func (r *RunRepo) TerminarTask(ctx context.Context, runID uuid.UUID, step dom.StepKey,
	attempt int, status dom.Status, exit *int, failure string, log string) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE task_runs
		SET status = $5, exit_code = $6, erro = $7, log = $8, terminado_em = now()
		WHERE run_id = $1 AND node_id = $2 AND attempt = $3 AND map_index = $4`,
		runID, step.Node, attempt, step.MapIndex, status, exit, failure, log)
	return err
}

// StepHasSucceeded answers whether this step, in this workflow, has ever
// finished well before -- in any earlier run.
//
// It is what decides whether the current run is that step's FIRST, information
// that goes into the step's environment and that the SDK uses to create the
// destination table. The alternative would be for the SDK to infer it from "the
// table does not exist", and then somebody drops the table by mistake and the
// next run believes it is the first.
//
// Per (workflow, step), not per workflow: a workflow with three fetchers writing
// to three tables would create only the first step's if the answer covered the
// whole workflow.
//
// `exceto` is the current run, excluded so the attempt in progress does not
// count as an earlier success.
func (r *RunRepo) StepHasSucceeded(ctx context.Context, workflowSlug, nodeID string, exceto uuid.UUID) (bool, error) {
	var existe bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM task_runs t
			JOIN runs rn ON rn.id = t.run_id
			WHERE rn.workflow_slug = $1
			  AND t.node_id = $2
			  AND t.run_id <> $3
			  AND t.status = $4
		)`, workflowSlug, nodeID, exceto, dom.StatusSuccess).Scan(&existe)
	return existe, err
}

// AlreadySucceeded returns the steps of THIS run that have already finished
// well, in any earlier attempt of it.
//
// It is what makes a run's retry re-run only what failed, the way a cleared DAG
// run does in Airflow. Before it, a retry re-ran the whole graph: a workflow
// with an expensive step beside a flaky one paid for the expensive one on every
// attempt, and any step that was not idempotent did its work twice.
//
// Per (run, node) and NOT per (workflow, node) -- that is StepHasSucceeded
// above, which answers a different question: whether this step has ever
// succeeded in an EARLIER run, which is what tells the SDK it is not the first.
// Confusing the two would make a step skip itself forever after its first good
// day.
func (r *RunRepo) AlreadySucceeded(ctx context.Context, runID uuid.UUID) (map[dom.StepKey]bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT node_id, map_index FROM task_runs
		WHERE run_id = $1 AND status = $2`, runID, dom.StatusSuccess)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[dom.StepKey]bool{}
	for rows.Next() {
		var k dom.StepKey
		if err := rows.Scan(&k.Node, &k.MapIndex); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// NodeStates returns each node's state on its LAST attempt.
//
// `DISTINCT ON` rather than max(attempt) in a subselect: the most recent
// attempt is the one that matters on screen, and an old attempt that failed must
// not paint the node red after the retry succeeded.
func (r *RunRepo) NodeStates(ctx context.Context, runID uuid.UUID) (map[string]NodeState, error) {
	rows, err := r.pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (node_id, map_index)
			       node_id, map_index, status, attempt, exit_code, erro,
			       iniciado_em, terminado_em, etapas, sdk_versao, saida
			FROM task_runs
			WHERE run_id = $1
			ORDER BY node_id, map_index, attempt DESC
		)
		SELECT
			node_id,
			-- The instance the CARD shows. A mapped step has several, and the
			-- one worth showing is the worst: three partitions green and one
			-- red is a node that did not do its job, and a green card over it
			-- is the badge that lies.
			(array_agg(status ORDER BY
				CASE status WHEN 'failed' THEN 0 WHEN 'running' THEN 1
				            WHEN 'skipped' THEN 2 WHEN 'success' THEN 3 ELSE 4 END,
				map_index))[1],
			max(attempt), min(exit_code),
			(array_agg(erro ORDER BY (erro = '') , map_index))[1],
			min(iniciado_em), max(terminado_em),
			-- The stages and the output belong to ONE instance, and showing a
			-- mapped step's phases would mean showing one of four arbitrarily.
			-- The unmapped case is every step that has ever had them.
			(array_agg(etapas ORDER BY map_index))[1],
			(array_agg(sdk_versao ORDER BY map_index))[1],
			(array_agg(saida ORDER BY map_index))[1],
			-- How many instances, and how many of them finished well. The [4]
			-- on the card is counted here rather than stored: nothing has to
			-- be kept in sync, and a wrong count cannot outlive a fixed query.
			count(*), count(*) FILTER (WHERE status = 'success'),
			min(map_index)
		FROM latest
		GROUP BY node_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]NodeState{}
	for rows.Next() {
		var e NodeState
		var ini, end *time.Time
		var stages, saida []byte
		var lowest int
		if err := rows.Scan(&e.NodeID, &e.Status, &e.Attempt, &e.ExitCode,
			&e.Err, &ini, &end, &stages, &e.SdkVersion, &saida,
			&e.Instances, &e.Done, &lowest); err != nil {
			return nil, err
		}
		if lowest == dom.Unmapped {
			// Not a mapped step. The counts are left at zero so the payload of
			// every workflow that has ever run is byte for byte what it was.
			e.Instances, e.Done = 0, 0
		}
		if len(saida) > 0 {
			e.Published = json.RawMessage(saida)
		}
		e.Stages = stepStages(stages, e.Status)
		if ini != nil && end != nil {
			d := end.Sub(*ini)
			e.DurationMs = d.Milliseconds()
		}
		out[e.NodeID] = e
	}
	return out, rows.Err()
}

// NodeState is a step's state, for the UI.
type NodeState struct {
	NodeID     string `json:"node_id"`
	Status     string `json:"status"`
	Attempt    int    `json:"attempt"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Err        string `json:"erro,omitempty"`
	DurationMs int64  `json:"duracao_ms"`

	// Etapas are the phases announced by an SDK step. Empty for a step that is
	// not an SDK one -- and that step's screen stays exactly as it was.
	Stages []Stage `json:"etapas,omitempty"`

	// SdkVersao is the version the step announced, empty when it is not an SDK step.
	SdkVersion string `json:"sdk_versao,omitempty"`

	// Published is what this step told the steps below it. Absent when it
	// published nothing, which is most steps.
	Published json.RawMessage `json:"saida,omitempty"`

	// Instances and Done are a MAPPED step's counts: how many instances there
	// are and how many finished well. Both zero for an unmapped step, which is
	// most of them, and the payload then carries neither field.
	Instances int `json:"instancias,omitempty"`
	Done      int `json:"instancias_ok,omitempty"`
}

// Etapa is one phase of an SDK step, for the screen.
type Stage struct {
	Index   int            `json:"indice"`
	Name    string         `json:"nome"`
	State   string         `json:"estado"`
	Ms      *int64         `json:"ms,omitempty"`
	At      string         `json:"em"`
	Numbers map[string]any `json:"numeros,omitempty"`
}

// stepStages reads the recorded phases and closes the ones left open.
//
// A step that died announces nothing: if it finished with a phase still
// `running`, that phase was INTERRUPTED, and showing it spinning forever would
// be the screen lying about a run that is already over.
//
// The rule applies on READ, not on write, because that way it also covers the
// case where whoever should have closed the phase is precisely who died.
func stepStages(data []byte, status string) []Stage {
	if len(data) == 0 {
		return nil
	}
	var stages []Stage
	if err := json.Unmarshal(data, &stages); err != nil {
		return nil
	}
	if terminal(status) {
		for i := range stages {
			if stages[i].State == "running" {
				stages[i].State = "aborted"
			}
		}
	}
	return stages
}

// terminal asks the STEP's question: these rows are task_runs.
func terminal(status string) bool { return dom.Status(status).TerminalStep() }

// StepLog is one attempt's output, for the run's screen.
type StepLog struct {
	NodeID     string
	Attempt    int
	Status     string
	ExitCode   *int
	Err        string
	Log        string
	DurationMs int64
}

// LogsDaRun returns the output of every attempt of every step, in execution
// order.
//
// EVERY attempt, not only the last: when a step passes on the second, what
// explains the first failure is precisely in the attempt the screen would
// discard.
func (r *RunRepo) LogsDaRun(ctx context.Context, runID uuid.UUID) ([]StepLog, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT node_id, attempt, status, exit_code, erro, log,
		       COALESCE(EXTRACT(EPOCH FROM (terminado_em - iniciado_em)) * 1000, 0)::bigint
		FROM task_runs
		WHERE run_id = $1
		ORDER BY iniciado_em NULLS LAST, node_id, attempt`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StepLog
	for rows.Next() {
		var p StepLog
		if err := rows.Scan(&p.NodeID, &p.Attempt, &p.Status, &p.ExitCode,
			&p.Err, &p.Log, &p.DurationMs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FailedStep returns the node and the output of the last attempt that
// failed.
//
// `ORDER BY iniciado_em DESC` and not `attempt DESC`: in a graph with several
// steps, the highest attempt may belong to a step that had already failed and
// been superseded — what matters is what failed LAST, which is where the run
// stopped.
//
// Absence is not an error: a run that died before any step started (a missing
// image, a cancelled queue) has no task_run at all, and the alert goes out
// without this part rather than not going out.
// FailedSteps returns EVERY step of this run that ended failed, newest first,
// with the end of its log.
//
// FailedStep above answers "which step should the run's alert name", and one is
// the right answer there: an alert has to fit in a phone notification. This one
// exists for `on_error`, where each declaring step gets its own message -- and a
// run with two parallel branches can legitimately have two of them fail.
//
// DISTINCT ON keeps the latest attempt per node. Without it a step that failed
// three times would produce three alerts saying the same thing.
func (r *RunRepo) FailedSteps(ctx context.Context, runID uuid.UUID) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (node_id) node_id, log
		FROM task_runs
		WHERE run_id = $1 AND status = $2
		ORDER BY node_id, attempt DESC`, runID, dom.StatusFailed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var node, log string
		if err := rows.Scan(&node, &log); err != nil {
			return nil, err
		}
		out[node] = log
	}
	return out, rows.Err()
}

func (r *RunRepo) FailedStep(ctx context.Context, runID uuid.UUID) (string, string, error) {
	var step, log string
	err := r.pool.QueryRow(ctx, `
		SELECT node_id, log
		FROM task_runs
		WHERE run_id = $1 AND status = $2
		ORDER BY iniciado_em DESC NULLS LAST, attempt DESC
		LIMIT 1`, runID, dom.StatusFailed).Scan(&step, &log)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return step, log, err
}
