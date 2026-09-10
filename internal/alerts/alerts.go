// Package alerts is the outbox: alerts are written where the failure is
// recorded, and delivered by somebody else.
//
// The defect it exists to fix is small and total. The dispatcher calls
// notify.Slack directly and, when the call fails, logs and carries on -- which
// is correct, because a Slack outage must not stop a pipeline, and which is
// also how the alert is LOST. No retry, no record, nothing on the screen to say
// one was meant to be sent. Whoever was supposed to be woken up simply is not,
// and nobody finds out.
//
// A separate pod that the dispatcher CALLS over HTTP would have exactly the
// same failure with more moving parts. What removes it is where the row is
// written: in the same transaction as the failure that justifies it. Either the
// run is recorded as out of attempts and the alert exists, or neither happened.
//
// The claim machinery is queue_items', on purpose -- same columns, same names,
// same visibility timeout. That table is already durable, claimable and
// retryable, it has been in production, and its failure modes are understood.
// Inventing a second vocabulary for the same idea would mean discovering them
// twice.
package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/notify"
)

// What raised the alert.
//
// It is stored rather than inferred from node_id being empty: "a run gave up"
// and "a step failed" are different messages, and a reader of this table should
// not have to guess which one a row is.
const (
	KindRun  = "run"
	KindStep = "step"
)

// The channels, taken from the DOMAIN rather than declared again here.
//
// The vocabulary belongs where a YAML's rules live, and having two copies is
// how a workflow ends up able to declare a destination this side cannot
// deliver to. `brevis publish` validates against the same list.
const ChannelSlack = wf.ChannelSlack

// Channels lists what an installation can deliver to.
func Channels() []string { return wf.AlertChannels() }

// KnownChannel reports whether the name is one this build can deliver to.
func KnownChannel(name string) bool {
	for _, c := range Channels() {
		if c == name {
			return true
		}
	}
	return false
}

// Pending is an alert about to be written. It carries the WHOLE message,
// resolved at the instant of the failure.
//
// Self-contained on purpose: the alert pod never reads runs or task_runs to
// build the text. Rebuilding it at delivery time would let a workflow
// republished in the meantime change the wording of an alert about a run that
// used the previous definition -- and an alert that does not describe what
// actually failed is worse than a late one.
type Pending struct {
	RunID   uuid.UUID
	Kind    string
	NodeID  string
	Channel string
	Payload notify.Alert
}

// Item is a claimed row, ready to deliver.
type Item struct {
	ID       int64
	RunID    uuid.UUID
	Kind     string
	NodeID   string
	Channel  string
	Payload  notify.Alert
	Attempts int
}

// Record is one row as the screen sees it, delivered or not.
type Record struct {
	Item
	Err         string
	DeliveredAt *time.Time
	GaveUpAt    *time.Time
	CreatedAt   time.Time
}

// State says where a record stands, in the words the screen uses.
func (r Record) State() string {
	switch {
	case r.DeliveredAt != nil:
		return "delivered"
	case r.GaveUpAt != nil:
		return "undelivered"
	default:
		return "sending"
	}
}

// Outbox operates on the alertas table.
type Outbox struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Outbox { return &Outbox{pool: pool} }

// WriteTx inserts inside a transaction the CALLER owns.
//
// This is the whole point of the package and the reason the signature takes a
// pgx.Tx rather than opening its own: the alert has to commit with the state
// change that justifies it, and a function that starts its own transaction
// cannot do that no matter how close together the two calls are.
func WriteTx(ctx context.Context, tx pgx.Tx, p Pending) error {
	payload, err := json.Marshal(p.Payload)
	if err != nil {
		return fmt.Errorf("serializando o alerta da run %s: %w", p.RunID, err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO alertas (run_id, tipo, node_id, canal, payload)
		VALUES ($1, $2, $3, $4, $5)`,
		p.RunID, p.Kind, p.NodeID, p.Channel, payload)
	if err != nil {
		return fmt.Errorf("gravando o alerta da run %s: %w", p.RunID, err)
	}
	return nil
}

// Claim takes up to `limit` alerts for this worker.
//
// FOR UPDATE SKIP LOCKED, the same as the run queue: several deliverers could
// compete without handing out the same row twice. In practice one process
// delivers, and that is deliberate -- Slack's rate limit is per workspace, so
// one sender is what keeps it from becoming the pipeline's problem.
func (o *Outbox) Claim(ctx context.Context, worker string, limit int) ([]Item, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := o.pool.Query(ctx, `
		UPDATE alertas
		SET reivindicado_em = now(), reivindicado_por = $1
		WHERE id IN (
			SELECT id FROM alertas
			WHERE reivindicado_em IS NULL
			  AND entregue_em IS NULL
			  AND desistiu_em IS NULL
			  AND disponivel_em <= now()
			ORDER BY disponivel_em, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, run_id, tipo, node_id, canal, payload, tentativas`,
		worker, limit)
	if err != nil {
		return nil, fmt.Errorf("reivindicando alertas: %w", err)
	}
	defer rows.Close()
	return scan(rows)
}

// Delivered marks the alert as sent, and clears the error: a row that succeeded
// on the third try should not still show the second try's failure.
func (o *Outbox) Delivered(ctx context.Context, id int64) error {
	_, err := o.pool.Exec(ctx, `
		UPDATE alertas
		SET entregue_em = now(), reivindicado_em = NULL, reivindicado_por = NULL,
		    tentativas = tentativas + 1, erro = ''
		WHERE id = $1`, id)
	return err
}

// Retry hands the alert back, available after `delay`, and RECORDS why.
//
// The error is kept on every attempt rather than only on the last, because the
// interesting case is a row that keeps failing for a changing reason -- a 429
// then a 403 is a rate limit followed by a revoked webhook, and only the second
// needs a human.
func (o *Outbox) Retry(ctx context.Context, id int64, cause error, delay time.Duration) error {
	_, err := o.pool.Exec(ctx, `
		UPDATE alertas
		SET reivindicado_em = NULL, reivindicado_por = NULL,
		    tentativas = tentativas + 1, erro = $2,
		    disponivel_em = now() + $3::interval
		WHERE id = $1`,
		id, message(cause), fmt.Sprintf("%d milliseconds", delay.Milliseconds()))
	return err
}

// GaveUp marks the alert as never going to arrive.
//
// The row is KEPT. "Raised, not delivered, 4 attempts, 403 from Slack" is the
// most useful thing this table produces: it is exactly the case where somebody
// is waiting for a message that is not coming, and deleting it makes that
// indistinguishable from an alert that was never raised at all.
func (o *Outbox) GaveUp(ctx context.Context, id int64, cause error) error {
	_, err := o.pool.Exec(ctx, `
		UPDATE alertas
		SET desistiu_em = now(), reivindicado_em = NULL, reivindicado_por = NULL,
		    tentativas = tentativas + 1, erro = $2
		WHERE id = $1`, id, message(cause))
	return err
}

// Recover frees alerts claimed longer ago than `limit` by a process that died.
//
// Without it, killing the alert pod mid-delivery leaves the row claimed
// forever: the alert was never sent and never will be, and the table says it is
// in progress. It is the same safety net queue_items has, for the same reason.
func (o *Outbox) Recover(ctx context.Context, limit time.Duration) (int, error) {
	tag, err := o.pool.Exec(ctx, `
		UPDATE alertas
		SET reivindicado_em = NULL, reivindicado_por = NULL
		WHERE reivindicado_em IS NOT NULL
		  AND reivindicado_em < now() - $1::interval`,
		fmt.Sprintf("%d milliseconds", limit.Milliseconds()))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ForRun lists a run's alerts for the screen, newest first.
func (o *Outbox) ForRun(ctx context.Context, runID uuid.UUID) ([]Record, error) {
	rows, err := o.pool.Query(ctx, `
		SELECT id, run_id, tipo, node_id, canal, payload, tentativas,
		       erro, entregue_em, desistiu_em, criado_em
		FROM alertas WHERE run_id = $1 ORDER BY id DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var r Record
		var payload []byte
		if err := rows.Scan(&r.ID, &r.RunID, &r.Kind, &r.NodeID, &r.Channel, &payload,
			&r.Attempts, &r.Err, &r.DeliveredAt, &r.GaveUpAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &r.Payload); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Pending counts what has not been delivered and has not been given up on, for
// the metrics endpoint. Read at scrape time, like the run queue's depth: the
// table is shared, and a local counter would report one process's opinion of it.
func (o *Outbox) Pending(ctx context.Context) (waiting, undelivered int, err error) {
	err = o.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE entregue_em IS NULL AND desistiu_em IS NULL),
			count(*) FILTER (WHERE desistiu_em IS NOT NULL)
		FROM alertas`).Scan(&waiting, &undelivered)
	return
}

func scan(rows pgx.Rows) ([]Item, error) {
	var out []Item
	for rows.Next() {
		var it Item
		var payload []byte
		if err := rows.Scan(&it.ID, &it.RunID, &it.Kind, &it.NodeID, &it.Channel,
			&payload, &it.Attempts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &it.Payload); err != nil {
			// A row whose payload cannot be read is not a reason to stop
			// draining the rest: the others are somebody's outage.
			return nil, fmt.Errorf("alert %d has an unreadable payload: %w", it.ID, err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// message keeps the stored error short. The column is TEXT and a driver can
// return a page of HTML on a bad gateway; the screen shows this in a cell.
func message(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 500 {
		return s[:500] + "…"
	}
	return s
}
