// Package queue is the engine's persistent queue.
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
	ID          int64
	RunID       uuid.UUID
	Priority    int
	AvailableAt time.Time

	// ClaimedAt is the claim this holder knows: reivindicado_em as the claim
	// wrote it. It is the fence on everything the holder does afterwards --
	// Renew, Done, Release -- because a reclaim always writes a new one, so the
	// pair (ID, ClaimedAt) names one claim and not merely one item.
	//
	// Zero means "no claim", which is what the sweep hands back: it leaves the
	// item unclaimed, and the orphan path's own Done and Release are fenced on
	// it staying that way.
	ClaimedAt time.Time
}

// claim is ClaimedAt as the fence's parameter: NULL for zero, so
// `reivindicado_em IS NOT DISTINCT FROM $2` matches an unclaimed item.
func (it Item) claim() any {
	if it.ClaimedAt.IsZero() {
		return nil
	}
	return it.ClaimedAt
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
func (q *Queue) Enqueue(ctx context.Context, runID uuid.UUID, priority int, availableAt time.Time) error {
	var when any = availableAt
	if availableAt.IsZero() {
		when = nil // COALESCE resolves to the database's now()
	}
	_, err := q.pool.Exec(ctx, `
		INSERT INTO queue_items (run_id, prioridade, disponivel_em)
		VALUES ($1, $2, COALESCE($3::timestamptz, now()))
		ON CONFLICT (run_id) DO NOTHING`,
		runID, priority, when)
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
//
// Claims are serialized across processes by an advisory lock (see claimLock).
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
	// That holds for ONE claimer. `em_voo` and `posicao` are computed from one
	// READ COMMITTED snapshot, and SKIP LOCKED only keeps two claimers off the
	// same ROW: neither sees the other's uncommitted claim, so two processes
	// claiming at once could each take a different item of a workflow with a
	// limit of 1. The advisory lock below makes the claims take turns; the
	// claim is a single short statement, so the wait is negligible.
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
	//
	// What is counted is the run's CONCURRENCY KEY, not its workflow (#32). A
	// workflow that names a group contends with every other member of it -- the
	// daily and the */15 that build the same dbt models -- and one that names
	// none contends with itself, under its slug, as it always did. The two
	// prefixes keep them apart: without them a group called `daily` and the
	// workflow `daily` would share one limit by accident of spelling.
	//
	// postgres.RunRepo.WaitingOn repeats this arithmetic to tell a queued run
	// what holds it (#95). A change to the key, the order or what counts as in
	// flight is a change there too.
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("reivindicando itens: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Transaction-scoped: released on commit, or on rollback if the claim fails.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, claimLock); err != nil {
		return nil, fmt.Errorf("reivindicando itens: %w", err)
	}
	rows, err := tx.Query(ctx, `
		WITH em_voo AS (
			SELECT COALESCE('g:' || r.grupo_concorrencia, 'w:' || r.workflow_slug) AS chave,
			       count(*) AS n
			FROM queue_items q
			JOIN runs r ON r.id = q.run_id
			WHERE q.reivindicado_em IS NOT NULL
			GROUP BY 1
		),
		elegiveis AS (
			SELECT q.id,
			       r.max_ativos,
			       COALESCE(v.n, 0) AS ja_em_voo,
			       row_number() OVER (
			           PARTITION BY COALESCE('g:' || r.grupo_concorrencia, 'w:' || r.workflow_slug)
			           ORDER BY q.prioridade DESC, q.disponivel_em, q.id
			       ) AS posicao
			FROM queue_items q
			JOIN runs r ON r.id = q.run_id
			LEFT JOIN em_voo v
			       ON v.chave = COALESCE('g:' || r.grupo_concorrencia, 'w:' || r.workflow_slug)
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
		RETURNING id, run_id, prioridade, disponivel_em, reivindicado_em`,
		worker, limite)
	if err != nil {
		return nil, fmt.Errorf("reivindicando itens: %w", err)
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.RunID, &it.Priority, &it.AvailableAt, &it.ClaimedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("reivindicando itens: %w", err)
	}
	return items, nil
}

// claimLock is the advisory lock key that serializes Claim. A constant, since
// the limits are counted across the whole queue; it is distinct from the
// hash-derived keys the postgres package uses.
const claimLock int64 = 0x62726576_636c6d31 // "brev" "clm1"

// Renew says the claim is still held. It is fenced on the claim itself, not on
// the item: a reclaim writes a new reivindicado_em, so a stale holder's renewal
// matches nothing -- and nothing is the answer that tells it it lost.
//
// `held` false with no error is that answer. An error is the database not
// answering, which says nothing about the claim either way.
func (q *Queue) Renew(ctx context.Context, it Item) (held bool, err error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE queue_items SET renovado_em = now()
		WHERE id = $1 AND reivindicado_em = $2`,
		it.ID, it.claim())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Done removes the item: the work finished and does not come back.
//
// Fenced on the claim (#121). A holder whose claim the sweep took and somebody
// else claimed again would otherwise delete the item that other attempt is
// running -- and the queue would forget a run still in flight.
func (q *Queue) Done(ctx context.Context, it Item) error {
	_, err := q.pool.Exec(ctx, `
		DELETE FROM queue_items
		WHERE id = $1 AND reivindicado_em IS NOT DISTINCT FROM $2`,
		it.ID, it.claim())
	return err
}

// Release returns the item to the queue, available after `atraso`.
//
// Used on retry and when a dispatcher is interrupted before finishing: the item
// becomes free again instead of staying stuck on a worker that died.
//
// Fenced on the claim, as Done is, so a stale holder cannot hand somebody
// else's run back to the queue.
func (q *Queue) Release(ctx context.Context, it Item, atraso time.Duration) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE queue_items
		SET reivindicado_em = NULL, reivindicado_por = NULL, renovado_em = NULL,
		    disponivel_em = now() + $3::interval
		WHERE id = $1 AND reivindicado_em IS NOT DISTINCT FROM $2`,
		it.ID, it.claim(), fmt.Sprintf("%d milliseconds", atraso.Milliseconds()))
	return err
}

// Recuperar returns to the queue the items whose holder has been silent longer
// than `limite`: no renewal for that long, or none at all and a claim that old.
// Measuring the claim alone took runs from live holders (#121).
//
// The items come back with no claim (a zero ClaimedAt), because that is what
// this leaves behind.
//
// It is the safety net against a dead worker: without it, an item claimed by a
// process that crashed would stay stuck forever. That was exactly the failure
// mode of the zombie runs that jammed pipelines for 33 days in the previous
// system.
func (q *Queue) Recover(ctx context.Context, limite time.Duration) ([]Item, error) {
	// It returns the items, not just the count: whoever recovers needs to know
	// WHICH runs were left dangling in order to fix their state too. With the
	// count alone, the item went back to the queue but the Run stayed "running"
	// forever -- the half of the bug this fixes.
	rows, err := q.pool.Query(ctx, `
		UPDATE queue_items
		SET reivindicado_em = NULL, reivindicado_por = NULL, renovado_em = NULL
		WHERE reivindicado_em IS NOT NULL
		  AND COALESCE(renovado_em, reivindicado_em) < now() - $1::interval
		RETURNING id, run_id, prioridade`,
		fmt.Sprintf("%d milliseconds", limite.Milliseconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.RunID, &it.Priority); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Size counts pending and claimed items, for observability.
func (q *Queue) Size(ctx context.Context) (pending, claimed int, err error) {
	err = q.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE reivindicado_em IS NULL),
			count(*) FILTER (WHERE reivindicado_em IS NOT NULL)
		FROM queue_items`).Scan(&pending, &claimed)
	return
}
