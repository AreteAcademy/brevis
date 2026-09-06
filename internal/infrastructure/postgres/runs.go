package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
)

// RunRepo persiste Runs e TaskRuns.
type RunRepo struct{ pool *Pool }

func NewRunRepo(p *Pool) *RunRepo { return &RunRepo{pool: p} }

// ErrJaExiste signals an idempotency-key collision. Typed so the caller can tell
// "I already created this" from a real error — the difference between a benign
// scheduler retry and a database failure.
var ErrJaExiste = errors.New("a run with this idempotency key already exists")

// Criar inserts the Run in CREATED.
//
// A collision on the idempotency_key unique becomes ErrJaExiste, not a generic
// error: it is section 29's case — the scheduler died after creating and tries
// again on the way back up.
func (r *RunRepo) Criar(ctx context.Context, run dom.Run) (dom.Run, error) {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	if run.Status == "" {
		run.Status = dom.StatusCreated
	}

	if run.TriggerType == "" {
		run.TriggerType = "manual"
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO runs (id, workflow_slug, idempotency_key, status, attempt, definicao,
		                  trigger_type, logical_date, params, max_ativos)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING criado_em`,
		run.ID, run.WorkflowSlug, run.IdempotencyKey, run.Status, run.Attempt, run.Definition,
		run.TriggerType, run.LogicalDate, paramsOuVazio(run.Params), run.MaxActive,
	).Scan(&run.CreatedAt)

	if err != nil {
		if ehViolacaoUnica(err) {
			return dom.Run{}, ErrJaExiste
		}
		return dom.Run{}, fmt.Errorf("criando run: %w", err)
	}
	return run, nil
}

// Transicionar applies the state change, validating BEFORE writing.
//
// The validation happens against the state read inside the transaction, with FOR
// UPDATE: reading outside it would let two dispatchers both read "queued" and
// both write "running".
func (r *RunRepo) Transicionar(ctx context.Context, id uuid.UUID, para dom.Status) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do commit

	var atual dom.Status
	if err := tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1 FOR UPDATE`, id).Scan(&atual); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("run %s does not exist", id)
		}
		return err
	}
	if err := dom.Validate(atual, para); err != nil {
		return fmt.Errorf("run %s: %w", id, err)
	}

	agora := time.Now()
	var iniciado, terminado any
	switch para {
	case dom.StatusRunning:
		iniciado = agora
	case dom.StatusSuccess, dom.StatusCanceled:
		terminado = agora
	}

	_, err = tx.Exec(ctx, `
		UPDATE runs SET
			status       = $2,
			iniciado_em  = COALESCE($3, CASE WHEN $2 = 'queued' THEN NULL ELSE iniciado_em END),
			terminado_em = COALESCE($4, CASE WHEN $2 = 'queued' THEN NULL ELSE terminado_em END)
		WHERE id = $1`,
		id, para, iniciado, terminado)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IncrementarTentativa bumps the counter when requeuing for a retry.
func (r *RunRepo) IncrementarTentativa(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`UPDATE runs SET attempt = attempt + 1 WHERE id = $1 RETURNING attempt`, id).Scan(&n)
	return n, err
}

// RegistrarErro stores the cause of the failure.
func (r *RunRepo) RegistrarErro(ctx context.Context, id uuid.UUID, msg string) error {
	_, err := r.pool.Exec(ctx, `UPDATE runs SET erro = $2 WHERE id = $1`, id, msg)
	return err
}

// Buscar le um Run.
func (r *RunRepo) Buscar(ctx context.Context, id uuid.UUID) (dom.Run, error) {
	var run dom.Run
	err := r.pool.QueryRow(ctx, `
		SELECT id, workflow_slug, idempotency_key, status, attempt, definicao,
		       trigger_type, logical_date, params, max_ativos, erro, criado_em, iniciado_em, terminado_em
		FROM runs WHERE id = $1`, id).
		Scan(&run.ID, &run.WorkflowSlug, &run.IdempotencyKey, &run.Status, &run.Attempt,
			&run.Definition, &run.TriggerType, &run.LogicalDate, &run.Params, &run.MaxActive,
			&run.Err, &run.CreatedAt, &run.StartedAt, &run.FinishedAt)
	return run, err
}

// ContarPorStatus is what PHASE 2's acceptance criterion measures.
func (r *RunRepo) ContarPorStatus(ctx context.Context) (map[dom.Status]int, error) {
	linhas, err := r.pool.Query(ctx, `SELECT status, count(*) FROM runs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	out := map[dom.Status]int{}
	for linhas.Next() {
		var s dom.Status
		var n int
		if err := linhas.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, linhas.Err()
}

// ContarPorTrigger shows where the runs came from — telling a backfill from a
// scheduled run is what section 12 asks for while investigating an incident.
func (r *RunRepo) ContarPorTrigger(ctx context.Context) (map[string]int, error) {
	linhas, err := r.pool.Query(ctx, `SELECT trigger_type, count(*) FROM runs GROUP BY trigger_type`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	out := map[string]int{}
	for linhas.Next() {
		var t string
		var n int
		if err := linhas.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, linhas.Err()
}

func ehViolacaoUnica(err error) bool {
	// 23505 = unique_violation
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// paramsOuVazio avoids writing NULL into a NOT NULL DEFAULT '{}' column: a Run
// with no params has empty params, not absent ones.
func paramsOuVazio(p map[string]string) map[string]string {
	if p == nil {
		return map[string]string{}
	}
	return p
}
