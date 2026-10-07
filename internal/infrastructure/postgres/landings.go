package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	exec "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
)

// RecordLandings keeps what one attempt declared it wrote.
//
// One statement for every target, through unnest, rather than one round trip
// per landing: a step may declare up to a hundred, and this runs as the step
// ends, while the next one waits for its slot.
//
// UPSERT, and the attempt is not in the key, for RecordLoad's reason: attempt 2
// replaces attempt 1's numbers for a target it lands again. A target only
// attempt 1 landed is left alone, because it did land -- the rows are in the
// warehouse whatever happened to the process afterwards.
func (r *RunRepo) RecordLandings(ctx context.Context, runID uuid.UUID, step dom.StepKey,
	workflow string, landings []exec.Landing) error {

	if len(landings) == 0 {
		return nil
	}
	targets := make([]string, len(landings))
	at := make([]time.Time, len(landings))
	rows := make([]*int64, len(landings))
	bytes := make([]*int64, len(landings))
	for i, l := range landings {
		targets[i], at[i], rows[i], bytes[i] = l.Target, l.At, l.Rows, l.Bytes
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO landings (
			run_id, node_id, map_index, target, workflow_slug,
			loaded_at, rows_written, bytes_written)
		SELECT $1, $2, $3, u.target, $4, u.loaded_at, u.rows_written, u.bytes_written
		FROM unnest($5::text[], $6::timestamptz[], $7::bigint[], $8::bigint[])
		     AS u(target, loaded_at, rows_written, bytes_written)
		ON CONFLICT (run_id, node_id, map_index, target) DO UPDATE SET
			workflow_slug = EXCLUDED.workflow_slug,
			loaded_at     = EXCLUDED.loaded_at,
			rows_written  = EXCLUDED.rows_written,
			bytes_written = EXCLUDED.bytes_written,
			legacy        = false`,
		runID, step.Node, step.MapIndex, workflow, targets, at, rows, bytes)
	return err
}
