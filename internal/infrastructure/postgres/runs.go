package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/alerts"
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
func (r *RunRepo) Create(ctx context.Context, run dom.Run) (dom.Run, error) {
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
		run.TriggerType, run.LogicalDate, paramsOrEmpty(run.Params), run.MaxActive,
	).Scan(&run.CreatedAt)

	if err != nil {
		if isUniqueViolation(err) {
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

	var current dom.Status
	if err := tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1 FOR UPDATE`, id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("run %s does not exist", id)
		}
		return err
	}
	if err := dom.Validate(current, para); err != nil {
		return fmt.Errorf("run %s: %w", id, err)
	}

	now := time.Now()
	var iniciado, terminado any
	switch para {
	case dom.StatusRunning:
		iniciado = now
	case dom.StatusSuccess, dom.StatusCanceled:
		terminado = now
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

// Attempt records the attempt that just finished and, when it was the LAST
// one, writes the alert it justifies -- in a single transaction.
//
// That transaction is the whole design of the alerts feature. Before it, the
// dispatcher incremented the counter and then called Slack: a process that died
// between the two left a run out of attempts with nobody told, and no record
// that anybody should have been. Now either the run is recorded as having spent
// its last attempt and the alert exists, or neither happened.
//
// `raise` is a builder rather than a value because building the message costs
// reads -- the run's details, the failing step's log -- and it may return
// nothing, which is what an installation with no channel configured does. It
// receives `gaveUp` because a step declaring `on_error.when: attempt` is
// announced on every failed attempt while the run-level alert waits for the
// last one.
func (r *RunRepo) Attempt(ctx context.Context, id uuid.UUID, budget int,
	raise func(attempt int, gaveUp bool) []alerts.Pending,
) (attempt int, gaveUp bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do commit

	if err := tx.QueryRow(ctx,
		`UPDATE runs SET attempt = attempt + 1 WHERE id = $1 RETURNING attempt`,
		id).Scan(&attempt); err != nil {
		return 0, false, err
	}

	gaveUp = budget > 0 && attempt >= budget
	if raise != nil {
		// Called on EVERY attempt, not only the last: a step may declare
		// `on_error.when: attempt`, and the builder is what decides. The
		// run-level alert waits for gaveUp on its own.
		for _, p := range raise(attempt, gaveUp) {
			if err := alerts.WriteTx(ctx, tx, p); err != nil {
				// The alert failing to write ROLLS THE ATTEMPT BACK, and that
				// is the correct trade rather than an oversight. The attempt
				// will be spent again by the retry; an alert dropped here is
				// gone for good, which is the failure this whole feature
				// exists to remove.
				return 0, false, err
			}
		}
	}
	return attempt, gaveUp, tx.Commit(ctx)
}

// RecordError stores the cause of the failure.
func (r *RunRepo) RecordError(ctx context.Context, id uuid.UUID, msg string) error {
	_, err := r.pool.Exec(ctx, `UPDATE runs SET erro = $2 WHERE id = $1`, id, msg)
	return err
}

// Buscar le um Run.
func (r *RunRepo) Get(ctx context.Context, id uuid.UUID) (dom.Run, error) {
	var run dom.Run
	err := r.pool.QueryRow(ctx, `
		SELECT id, workflow_slug, idempotency_key, status, attempt, definicao,
		       trigger_type, logical_date, params, max_ativos, erro, criado_em, iniciado_em, terminado_em,
		       auto_params
		FROM runs WHERE id = $1`, id).
		Scan(&run.ID, &run.WorkflowSlug, &run.IdempotencyKey, &run.Status, &run.Attempt,
			&run.Definition, &run.TriggerType, &run.LogicalDate, &run.Params, &run.MaxActive,
			&run.Err, &run.CreatedAt, &run.StartedAt, &run.FinishedAt, &run.Auto)
	return run, err
}

// RecordAuto writes the run's automatic params, and reads back what it needs to
// compute them in the SAME statement.
//
// One round trip, and one transaction, because the parts are not independent:
// `started_at` is set by the transition that just happened, and
// `previous_error` is about the run before this one. Reading them separately
// would let a concurrent retry of that previous run change the answer between
// the two reads, and the run would carry a `previous_error` nobody can
// reproduce.
//
// The previous run is the one with the closest EARLIER logical_date, falling
// back to creation order for a workflow with no schedule. A manual run in the
// middle of a nightly is not "the previous run" of that nightly.
func (r *RunRepo) RecordAuto(ctx context.Context, id uuid.UUID,
	window func(slot time.Time) (start, end time.Time),
) (dom.AutoParams, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return dom.AutoParams{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do commit

	var run dom.Run
	if err := tx.QueryRow(ctx, `
		SELECT workflow_slug, trigger_type, logical_date, iniciado_em
		FROM runs WHERE id = $1`, id).
		Scan(&run.WorkflowSlug, &run.TriggerType, &run.LogicalDate, &run.StartedAt); err != nil {
		return dom.AutoParams{}, err
	}

	var prev dom.Previous
	// A pointer, because both subqueries return NULL when there is no previous
	// run -- which is every workflow's first, and scanning that into a bool
	// fails rather than answering "no".
	var failed *bool
	// COALESCE on the ordering key so a workflow with no schedule falls back to
	// creation order rather than dropping out of the comparison entirely.
	err = tx.QueryRow(ctx, `
		SELECT
			(SELECT status <> 'success' FROM runs p
			  WHERE p.workflow_slug = $1 AND p.id <> $2
			    AND COALESCE(p.logical_date, p.criado_em) < COALESCE($3::timestamptz, now())
			  ORDER BY COALESCE(p.logical_date, p.criado_em) DESC LIMIT 1),
			(SELECT COALESCE(p.logical_date, p.criado_em) FROM runs p
			  WHERE p.workflow_slug = $1 AND p.id <> $2 AND p.status = 'success'
			    AND COALESCE(p.logical_date, p.criado_em) < COALESCE($3::timestamptz, now())
			  ORDER BY COALESCE(p.logical_date, p.criado_em) DESC LIMIT 1)`,
		run.WorkflowSlug, id, run.LogicalDate).Scan(&failed, &prev.SuccessAt)
	if err != nil {
		return dom.AutoParams{}, err
	}
	prev.Failed = failed != nil && *failed

	var interval dom.Interval
	if run.LogicalDate != nil && window != nil {
		interval.Start, interval.End = window(*run.LogicalDate)
	}

	started := time.Time{}
	if run.StartedAt != nil {
		started = *run.StartedAt
	}
	auto := dom.Auto(run, started, prev, interval)

	if _, err := tx.Exec(ctx,
		`UPDATE runs SET auto_params = $2 WHERE id = $1`, id, auto); err != nil {
		return dom.AutoParams{}, err
	}
	return auto, tx.Commit(ctx)
}

// CountByStatus is what PHASE 2's acceptance criterion measures.
func (r *RunRepo) CountByStatus(ctx context.Context) (map[dom.Status]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT status, count(*) FROM runs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[dom.Status]int{}
	for rows.Next() {
		var s dom.Status
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// CountByTrigger shows where the runs came from — telling a backfill from a
// scheduled run is what investigating an incident needs.
func (r *RunRepo) CountByTrigger(ctx context.Context) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT trigger_type, count(*) FROM runs GROUP BY trigger_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	// 23505 = unique_violation
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// paramsOrEmpty avoids writing NULL into a NOT NULL DEFAULT '{}' column: a Run
// with no params has empty params, not absent ones.
func paramsOrEmpty(p map[string]string) map[string]string {
	if p == nil {
		return map[string]string{}
	}
	return p
}
