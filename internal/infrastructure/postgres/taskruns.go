package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/google/uuid"

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
func (r *RunRepo) IniciarTask(ctx context.Context, runID uuid.UUID, nodeID string, tentativa int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, iniciado_em)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (run_id, node_id, attempt) DO UPDATE
		SET status = EXCLUDED.status, iniciado_em = now(), terminado_em = NULL, erro = ''`,
		uuid.New(), runID, nodeID, dom.StatusRunning, tentativa)
	return err
}

// RegistrarEtapas records the advance of an SDK step's phases.
//
// It overwrites the whole array rather than appending: the runner's collector
// already keeps ONE entry per phase, with its current state, and the screen
// wants four boxes rather than a diary.
func (r *RunRepo) RegistrarEtapas(ctx context.Context, runID uuid.UUID, nodeID string,
	tentativa int, sdkVersao string, etapas json.RawMessage) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE task_runs
		SET etapas = $4, sdk_versao = COALESCE(NULLIF($5, ''), sdk_versao)
		WHERE run_id = $1 AND node_id = $2 AND attempt = $3`,
		runID, nodeID, tentativa, etapas, sdkVersao)
	return err
}

// TerminarTask records the outcome.
func (r *RunRepo) TerminarTask(ctx context.Context, runID uuid.UUID, nodeID string,
	tentativa int, status dom.Status, exit *int, erro string, log string) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE task_runs
		SET status = $4, exit_code = $5, erro = $6, log = $7, terminado_em = now()
		WHERE run_id = $1 AND node_id = $2 AND attempt = $3`,
		runID, nodeID, tentativa, status, exit, erro, log)
	return err
}

// PassoJaTeveSucesso answers whether this step, in this workflow, has ever
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
func (r *RunRepo) PassoJaTeveSucesso(ctx context.Context, workflowSlug, nodeID string, exceto uuid.UUID) (bool, error) {
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

// EstadoDosNos returns each node's state on its LAST attempt.
//
// `DISTINCT ON` rather than max(attempt) in a subselect: the most recent
// attempt is the one that matters on screen, and an old attempt that failed must
// not paint the node red after the retry succeeded.
func (r *RunRepo) EstadoDosNos(ctx context.Context, runID uuid.UUID) (map[string]NodeState, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (node_id)
		       node_id, status, attempt, exit_code, erro, iniciado_em, terminado_em,
		       etapas, sdk_versao
		FROM task_runs
		WHERE run_id = $1
		ORDER BY node_id, attempt DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	out := map[string]NodeState{}
	for linhas.Next() {
		var e NodeState
		var ini, fim *time.Time
		var etapas []byte
		if err := linhas.Scan(&e.NodeID, &e.Status, &e.Attempt, &e.ExitCode,
			&e.Err, &ini, &fim, &etapas, &e.SdkVersao); err != nil {
			return nil, err
		}
		e.Etapas = etapasDoPasso(etapas, e.Status)
		if ini != nil && fim != nil {
			d := fim.Sub(*ini)
			e.DuracaoMs = d.Milliseconds()
		}
		out[e.NodeID] = e
	}
	return out, linhas.Err()
}

// NodeState is a step's state, for the UI.
type NodeState struct {
	NodeID    string `json:"node_id"`
	Status    string `json:"status"`
	Attempt   int    `json:"attempt"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Err       string `json:"erro,omitempty"`
	DuracaoMs int64  `json:"duracao_ms"`

	// Etapas are the phases announced by an SDK step. Empty for a step that is
	// not an SDK one -- and that step's screen stays exactly as it was.
	Etapas []Etapa `json:"etapas,omitempty"`

	// SdkVersao is the version the step announced, empty when it is not an SDK step.
	SdkVersao string `json:"sdk_versao,omitempty"`
}

// Etapa is one phase of an SDK step, for the screen.
type Etapa struct {
	Indice  int            `json:"indice"`
	Nome    string         `json:"nome"`
	State   string         `json:"estado"`
	Ms      *int64         `json:"ms,omitempty"`
	Em      string         `json:"em"`
	Numeros map[string]any `json:"numeros,omitempty"`
}

// etapasDoPasso reads the recorded phases and closes the ones left open.
//
// A step that died announces nothing: if it finished with a phase still
// `running`, that phase was INTERRUPTED, and showing it spinning forever would
// be the screen lying about a run that is already over.
//
// The rule applies on READ, not on write, because that way it also covers the
// case where whoever should have closed the phase is precisely who died.
func etapasDoPasso(dados []byte, status string) []Etapa {
	if len(dados) == 0 {
		return nil
	}
	var etapas []Etapa
	if err := json.Unmarshal(dados, &etapas); err != nil {
		return nil
	}
	if terminal(status) {
		for i := range etapas {
			if etapas[i].State == "running" {
				etapas[i].State = "aborted"
			}
		}
	}
	return etapas
}

func terminal(status string) bool {
	switch dom.Status(status) {
	case dom.StatusSuccess, dom.StatusFailed, dom.StatusCanceled:
		return true
	}
	return false
}

// StepLog is one attempt's output, for the run's screen.
type StepLog struct {
	NodeID    string
	Attempt   int
	Status    string
	ExitCode  *int
	Err       string
	Log       string
	DuracaoMs int64
}

// LogsDaRun returns the output of every attempt of every step, in execution
// order.
//
// EVERY attempt, not only the last: when a step passes on the second, what
// explains the first failure is precisely in the attempt the screen would
// discard.
func (r *RunRepo) LogsDaRun(ctx context.Context, runID uuid.UUID) ([]StepLog, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT node_id, attempt, status, exit_code, erro, log,
		       COALESCE(EXTRACT(EPOCH FROM (terminado_em - iniciado_em)) * 1000, 0)::bigint
		FROM task_runs
		WHERE run_id = $1
		ORDER BY iniciado_em NULLS LAST, node_id, attempt`, runID)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []StepLog
	for linhas.Next() {
		var p StepLog
		if err := linhas.Scan(&p.NodeID, &p.Attempt, &p.Status, &p.ExitCode,
			&p.Err, &p.Log, &p.DuracaoMs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, linhas.Err()
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
func (r *RunRepo) FailedStep(ctx context.Context, runID uuid.UUID) (string, string, error) {
	var passo, log string
	err := r.pool.QueryRow(ctx, `
		SELECT node_id, log
		FROM task_runs
		WHERE run_id = $1 AND status = $2
		ORDER BY iniciado_em DESC NULLS LAST, attempt DESC
		LIMIT 1`, runID, dom.StatusFailed).Scan(&passo, &log)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return passo, log, err
}
