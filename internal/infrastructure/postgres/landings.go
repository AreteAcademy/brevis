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
// The configuration a landing was written with (Landing.Config) is stored
// once by its hash, in the same transaction, and the landing points at it.
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
	hashes := make([]*string, len(landings))
	configs := map[string][]byte{}
	for i, l := range landings {
		targets[i], at[i], rows[i], bytes[i] = l.Target, l.At, l.Rows, l.Bytes
		if l.Config != nil {
			hashes[i] = &l.Config.Hash
			configs[l.Config.Hash] = l.Config.Body
		}
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The configuration first, once by its hash: the landings point at it.
	for hash, body := range configs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO writer_configs (hash, body, first_seen) VALUES ($1, $2, now())
			ON CONFLICT (hash) DO NOTHING`, hash, body); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO landings (
			run_id, node_id, map_index, target, workflow_slug,
			loaded_at, rows_written, bytes_written, config_hash)
		SELECT $1, $2, $3, u.target, $4, u.loaded_at, u.rows_written, u.bytes_written, u.config_hash
		FROM unnest($5::text[], $6::timestamptz[], $7::bigint[], $8::bigint[], $9::text[])
		     AS u(target, loaded_at, rows_written, bytes_written, config_hash)
		ON CONFLICT (run_id, node_id, map_index, target) DO UPDATE SET
			workflow_slug = EXCLUDED.workflow_slug,
			loaded_at     = EXCLUDED.loaded_at,
			rows_written  = EXCLUDED.rows_written,
			bytes_written = EXCLUDED.bytes_written,
			config_hash   = EXCLUDED.config_hash,
			legacy        = false`,
		runID, step.Node, step.MapIndex, workflow, targets, at, rows, bytes, hashes); err != nil {
		return err
	}

	// The step's configuration history: a line only where the hash differs
	// from the step's last one for that target (see migration 00020).
	if len(configs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO step_config_history (target, workflow_slug, node_id, config_hash, first_at, run_id)
			SELECT u.target, $1, $2, u.config_hash, u.loaded_at, $3
			FROM unnest($4::text[], $5::timestamptz[], $6::text[]) AS u(target, loaded_at, config_hash)
			WHERE u.config_hash IS NOT NULL
			  AND u.config_hash IS DISTINCT FROM (
				SELECT h.config_hash FROM step_config_history h
				WHERE h.target = u.target AND h.workflow_slug = $1 AND h.node_id = $2
				ORDER BY h.first_at DESC LIMIT 1)
			ON CONFLICT DO NOTHING`,
			workflow, step.Node, runID, targets, at, hashes); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
