// Package queue is the persistent queue from §8 of the plan.
//
// The queue lives in Postgres, not in an in-memory channel: "never depend
// exclusively on an in-memory channel for critical jobs". A process that dies
// with items in a channel loses work; one that dies with items in a table does
// not.
//
// The claim uses `FOR UPDATE SKIP LOCKED`, the standard pattern for a queue in
// Postgres: several dispatchers compete over the same table without blocking
// each other and without handing out the same item twice. The alternative -- a
// SELECT followed by an UPDATE -- has a race between the two statements.
package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Item is one entry in the queue.
type Item struct {
	ID           int64
	RunID        uuid.UUID
	Prioridade   int
	DisponivelEm time.Time
}

// Queue operates on queue_items.
type Queue struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Queue { return &Queue{pool: pool} }

// Enqueue puts a run in the queue. `disponivelEm` zero means NOW, measured by
// the DATABASE's clock.
//
// `ON CONFLICT DO NOTHING` on run_id's unique: enqueueing the same run twice is
// a no-op, not an error. That is the behaviour §29 asks for -- the operation
// tolerates repetition.
//
// The clock difference matters: the process's clock can be a few milliseconds
// ahead of Postgres's, and an item written with the application's `time.Now()`
// stays invisible until the database catches up. Nothing is lost -- the next
// cycle picks it up -- but it is unexplainable latency, and it is what made a
// concurrency test hand out 4 items where 5 were ready.
func (q *Queue) Enqueue(ctx context.Context, runID uuid.UUID, prioridade int, disponivelEm time.Time) error {
	var quando any = disponivelEm
	if disponivelEm.IsZero() {
		quando = nil // COALESCE resolves to the database's now()
	}
	_, err := q.pool.Exec(ctx, `
		INSERT INTO queue_items (run_id, prioridade, disponivel_em)
		VALUES ($1, $2, COALESCE($3::timestamptz, now()))
		ON CONFLICT (run_id) DO NOTHING`,
		runID, prioridade, quando)
	if err != nil {
		return fmt.Errorf("enfileirando run %s: %w", runID, err)
	}
	return nil
}

// Claim claims up to `limite` items for this worker.
//
// The limit is how concurrency is enforced: the dispatcher asks only for the
// slots it has free. There is no path in which more items leave the queue than
// concurrency allows, because whoever counts the slots is whoever asks.
func (q *Queue) Claim(ctx context.Context, worker string, limite int) ([]Item, error) {
	if limite <= 0 {
		return nil, nil
	}

	// The per-workflow limit is enforced HERE, inside the claim query itself,
	// for the same reason global concurrency is enforced in the request: there
	// is no path in which more items leave the queue than allowed. Claiming and
	// then handing back would be a window in which two dispatchers had already
	// taken the same workflow.
	//
	// `em_voo` counts CLAIMED items, not runs in `running`: between the claim
	// and the state transition there is an instant where the run is still
	// `queued`, and counting by status would open exactly that gap.
	//
	// `posicao` is what prevents the second problem: without it, three items of
	// the same workflow with a limit of 1 would ALL come out in the same batch,
	// because the count does not change mid-query. With the per-workflow
	// numbering, an item only passes if `em_voo + its position` fits the
	// limit.
	linhas, err := q.pool.Query(ctx, `
		WITH em_voo AS (
			SELECT r.workflow_slug, count(*) AS n
			FROM queue_items q
			JOIN runs r ON r.id = q.run_id
			WHERE q.reivindicado_em IS NOT NULL
			GROUP BY r.workflow_slug
		),
		elegiveis AS (
			SELECT q.id,
			       r.max_ativos,
			       COALESCE(v.n, 0) AS ja_em_voo,
			       row_number() OVER (
			           PARTITION BY r.workflow_slug
			           ORDER BY q.prioridade DESC, q.disponivel_em, q.id
			       ) AS posicao
			FROM queue_items q
			JOIN runs r ON r.id = q.run_id
			LEFT JOIN em_voo v ON v.workflow_slug = r.workflow_slug
			WHERE q.reivindicado_em IS NULL
			  AND q.disponivel_em <= now()
		)
		UPDATE queue_items
		SET reivindicado_em = now(), reivindicado_por = $1
		WHERE id IN (
			SELECT q.id
			FROM queue_items q
			JOIN elegiveis e ON e.id = q.id
			WHERE q.reivindicado_em IS NULL
			  AND q.disponivel_em <= now()
			  AND (e.max_ativos = 0 OR e.ja_em_voo + e.posicao <= e.max_ativos)
			ORDER BY q.prioridade DESC, q.disponivel_em, q.id
			LIMIT $2
			FOR UPDATE OF q SKIP LOCKED
		)
		RETURNING id, run_id, prioridade, disponivel_em`,
		worker, limite)
	if err != nil {
		return nil, fmt.Errorf("reivindicando itens: %w", err)
	}
	defer linhas.Close()

	var itens []Item
	for linhas.Next() {
		var it Item
		if err := linhas.Scan(&it.ID, &it.RunID, &it.Prioridade, &it.DisponivelEm); err != nil {
			return nil, err
		}
		itens = append(itens, it)
	}
	return itens, linhas.Err()
}

// Done removes the item: the work finished and does not come back.
func (q *Queue) Done(ctx context.Context, id int64) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM queue_items WHERE id = $1`, id)
	return err
}

// Release returns the item to the queue, available after `atraso`.
//
// Used on retry and when a dispatcher is interrupted before finishing: the item
// becomes free again instead of staying stuck on a worker that died.
func (q *Queue) Release(ctx context.Context, id int64, atraso time.Duration) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE queue_items
		SET reivindicado_em = NULL, reivindicado_por = NULL, disponivel_em = now() + $2::interval
		WHERE id = $1`,
		id, fmt.Sprintf("%d milliseconds", atraso.Milliseconds()))
	return err
}

// Recuperar returns to the queue the items claimed longer ago than `limite`.
//
// It is the safety net against a dead worker: without it, an item claimed by a
// process that crashed would stay stuck forever. That was exactly the failure
// mode of the zombie runs that jammed pipelines for 33 days in the previous
// system.
func (q *Queue) Recuperar(ctx context.Context, limite time.Duration) ([]Item, error) {
	// It returns the items, not just the count: whoever recovers needs to know
	// WHICH runs were left dangling in order to fix their state too. With the
	// count alone, the item went back to the queue but the Run stayed "running"
	// forever -- the half of the bug this fixes.
	linhas, err := q.pool.Query(ctx, `
		UPDATE queue_items
		SET reivindicado_em = NULL, reivindicado_por = NULL
		WHERE reivindicado_em IS NOT NULL
		  AND reivindicado_em < now() - $1::interval
		RETURNING id, run_id, prioridade`,
		fmt.Sprintf("%d milliseconds", limite.Milliseconds()))
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []Item
	for linhas.Next() {
		var it Item
		if err := linhas.Scan(&it.ID, &it.RunID, &it.Prioridade); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, linhas.Err()
}

// Tamanho counts pending and claimed items, for observability.
func (q *Queue) Tamanho(ctx context.Context) (pendentes, reivindicados int, err error) {
	err = q.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE reivindicado_em IS NULL),
			count(*) FILTER (WHERE reivindicado_em IS NOT NULL)
		FROM queue_items`).Scan(&pendentes, &reivindicados)
	return
}
