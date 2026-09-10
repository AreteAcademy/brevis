package postgres

import (
	"context"
	"fmt"
	"time"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
)

// Retention has TWO levels, because the cost and the meaning are not in the
// same place.
//
// Measured on a year of hourly runs across forty workflows -- 350,000 runs:
//
//	task_runs     339 MB   log 119, etapas 113, saida 20, indexes 35
//	runs          141 MB   auto_params 38, definicao 30, indexes 31
//	load_metrics   81 MB
//
// Three quarters of the biggest table is BULK: the text a step printed and the
// JSON of its phases. Emptying those columns takes task_runs from 339 MB to
// 92 MB and deletes NOTHING -- every run still opens, with its steps, statuses,
// timings, exit codes and errors. What is lost is the log of a run from March,
// which is read by nobody, and its phase boxes.
//
// So the first level TRIMS and the second DELETES, and the gap between them is
// where almost all the value is. A policy with only the second would be
// throwing away runs to reclaim space that was never in the run.
//
// load_metrics is untouched by both. It has no foreign key for exactly this
// reason: the summary outlives the detail, which is the only thing that makes a
// year-long trend possible on a ninety-day log.
type Retention struct {
	// TrimAfter empties log, etapas and saida on runs older than this.
	TrimAfter time.Duration

	// PurgeAfter deletes the run outright, and with it its task_runs, its queue
	// items and its alerts -- but NOT its row in load_metrics.
	//
	// Zero means never, which is a real answer: a run's status and duration are
	// small, and an installation may want them forever.
	PurgeAfter time.Duration

	// Batch is how many runs one statement touches. The whole point is that a
	// prune of a neglected database must not take a lock for a minute: a
	// hundred short transactions beat one long one, and an interrupted prune
	// leaves a consistent database with less work left to do.
	Batch int
}

// PruneReport is what a prune did, or would do.
type PruneReport struct {
	Trimmed int64
	Purged  int64
	DryRun  bool
}

func (r PruneReport) String() string {
	verb := "trimmed %d run(s), purged %d"
	if r.DryRun {
		verb = "would trim %d run(s), would purge %d"
	}
	return fmt.Sprintf(verb, r.Trimmed, r.Purged)
}

// DefaultBatch is the batch size when none is given. Large enough that a
// backlog clears in reasonable time, small enough that no single statement
// holds a lock long.
const DefaultBatch = 5000

// Validate refuses a policy that would destroy more than it says.
//
// The checks are here rather than in the flag parsing because the scheduler
// could call this too one day, and a guard that lives in one caller is a guard
// the second caller does not have.
func (p Retention) Validate() error {
	if p.TrimAfter <= 0 {
		return fmt.Errorf("trim-after must be positive; %s would empty the log of a run that is still going", p.TrimAfter)
	}
	if p.PurgeAfter != 0 && p.PurgeAfter < p.TrimAfter {
		return fmt.Errorf("purge-after (%s) is shorter than trim-after (%s): the runs would be gone before their logs were",
			p.PurgeAfter, p.TrimAfter)
	}
	return nil
}

// Prune applies the retention policy.
//
// `dry` reports what it would do and changes nothing, which is what makes the
// first run of this on a real installation something an operator can look at
// before believing.
func (r *RunRepo) Prune(ctx context.Context, p Retention, dry bool) (PruneReport, error) {
	if err := p.Validate(); err != nil {
		return PruneReport{}, err
	}
	batch := p.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	out := PruneReport{DryRun: dry}

	trimmed, err := r.trim(ctx, p.TrimAfter, batch, dry)
	if err != nil {
		return out, fmt.Errorf("trim: %w", err)
	}
	out.Trimmed = trimmed

	if p.PurgeAfter > 0 {
		purged, err := r.purge(ctx, p.PurgeAfter, batch, dry)
		if err != nil {
			return out, fmt.Errorf("purge: %w", err)
		}
		out.Purged = purged
	}
	return out, nil
}

// trim empties the bulk columns on old task_runs.
//
// The `log <> ”` half of the eligibility query is not an optimisation.
// Removing it does not make this slower, it makes it NOT TERMINATE: every batch
// finds the same rows still eligible, reports a full batch, and the loop goes
// round forever rewriting the same hundred thousand rows.
//
// Found by deleting the clause and watching the test hang, which is a sharper
// argument than the one written here first -- that it would merely collect a
// dead tuple per row per night, on a table whose whole problem was size.
//
// With it, the second prune of the same day touches nothing and reports zero.
func (r *RunRepo) trim(ctx context.Context, after time.Duration, batch int, dry bool) (int64, error) {
	const eligible = `
		SELECT t.id
		FROM task_runs t
		JOIN runs r ON r.id = t.run_id
		WHERE r.criado_em < now() - $1::interval
		  AND (t.log <> '' OR t.etapas <> '[]'::jsonb OR t.saida IS NOT NULL)
		LIMIT $2`

	if dry {
		var n int64
		err := r.pool.QueryRow(ctx,
			`SELECT count(*) FROM task_runs t JOIN runs r ON r.id = t.run_id
			 WHERE r.criado_em < now() - $1::interval
			   AND (t.log <> '' OR t.etapas <> '[]'::jsonb OR t.saida IS NOT NULL)`,
			interval(after)).Scan(&n)
		return n, err
	}

	var total int64
	for {
		tag, err := r.pool.Exec(ctx, `
			UPDATE task_runs SET log = '', etapas = '[]'::jsonb, saida = NULL
			WHERE id IN (`+eligible+`)`, interval(after), batch)
		if err != nil {
			return total, err
		}
		n := tag.RowsAffected()
		total += n
		if n < int64(batch) {
			return total, nil
		}
		// A cancelled prune is a finished prune with work left over, not a
		// half-written one: every batch is its own transaction.
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// purge deletes whole runs. task_runs, queue_items and alertas follow by
// cascade; load_metrics does NOT, which is the point of it having no foreign
// key.
func (r *RunRepo) purge(ctx context.Context, after time.Duration, batch int, dry bool) (int64, error) {
	// The statuses come from the DOMAIN, not from three strings typed into SQL.
	//
	// A run that is still in flight is never purged, however old the row looks:
	// a queue item stuck since March is a bug worth seeing, and deleting it
	// would hide it along with whatever a dispatcher still believes it owns.
	// Which states those are is a question about ownership, and the answer
	// lives in run.Settled -- so a state added there arrives here with no edit.
	// As []string, not []dom.Status: pgx encodes a named string type as an
	// unknown OID and the comparison silently matches nothing.
	settled := make([]string, 0, 4)
	for _, st := range dom.Settled() {
		settled = append(settled, string(st))
	}

	if dry {
		var n int64
		err := r.pool.QueryRow(ctx,
			`SELECT count(*) FROM runs WHERE criado_em < now() - $1::interval AND status = ANY($2)`,
			interval(after), settled).Scan(&n)
		return n, err
	}

	var total int64
	for {
		tag, err := r.pool.Exec(ctx, `
			DELETE FROM runs WHERE id IN (
				SELECT id FROM runs
				WHERE criado_em < now() - $1::interval
				  AND status = ANY($2)
				LIMIT $3)`, interval(after), settled, batch)
		if err != nil {
			return total, err
		}
		n := tag.RowsAffected()
		total += n
		if n < int64(batch) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// interval renders a Go duration for Postgres.
//
// As seconds, and not as `720h`: Postgres reads `h` fine but not Go's `1h30m0s`
// composite, and the first duration that was not a whole number of hours would
// be an error nobody predicted from reading this.
func interval(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(d.Seconds()))
}
