package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
)

// GatewayRepo stores what gateways were published with.
type GatewayRepo struct{ pool *Pool }

func NewGatewayRepo(p *Pool) *GatewayRepo { return &GatewayRepo{pool: p} }

// Publish replaces one gateway's destinations and streams with the manifest's,
// in one transaction: a stream removed from the config is gone after this, and
// a failure leaves the previous publish whole rather than half of either.
// Other gateways' rows are not touched.
//
// Each stream's configuration is stored once by its hash, and its history
// gains a line only when that hash differs from the last one recorded.
//
// The manifest must already have been through catalog.ParseManifest.
func (r *GatewayRepo) Publish(ctx context.Context, m catalog.Manifest, at time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM gateway_destinations WHERE gateway = $1`, m.Gateway); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_streams WHERE gateway = $1`, m.Gateway); err != nil {
		return err
	}
	for _, st := range m.Streams {
		if err := publishStream(ctx, tx, m.Gateway, st, at); err != nil {
			return err
		}
		for _, d := range st.Destinations {
			if _, err := tx.Exec(ctx, `
				INSERT INTO gateway_destinations
				    (gateway, stream_path, role, kind, target, note, routes, published_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				m.Gateway, st.Path, d.Role, d.Kind, d.Target, d.Note, d.Routes, at); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// publishStream stores one stream's configuration and its current row.
func publishStream(ctx context.Context, tx pgx.Tx, gateway string, st catalog.ManifestStream, at time.Time) error {
	hash, body := st.ConfigHash()
	var ref *string
	if hash != "" {
		ref = &hash
		if _, err := tx.Exec(ctx, `
			INSERT INTO writer_configs (hash, body, first_seen) VALUES ($1, $2, $3)
			ON CONFLICT (hash) DO NOTHING`, hash, body, at); err != nil {
			return err
		}
		// A line only when the configuration differs from the LAST one: going
		// back to an earlier configuration is a change too, and is recorded.
		if _, err := tx.Exec(ctx, `
			INSERT INTO gateway_config_history (gateway, stream_path, config_hash, published_at)
			SELECT $1, $2, $3, $4
			WHERE $3 IS DISTINCT FROM (
				SELECT config_hash FROM gateway_config_history
				WHERE gateway = $1 AND stream_path = $2
				ORDER BY published_at DESC LIMIT 1)
			ON CONFLICT DO NOTHING`, gateway, st.Path, hash, at); err != nil {
			return err
		}
	}
	var notes []byte
	if len(st.Notes) > 0 {
		var err error
		if notes, err = json.Marshal(st.Notes); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO gateway_streams (gateway, stream_path, name, config_hash, notes, published_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, gateway, st.Path, st.Name, ref, notes, at)
	return err
}

// Unpublish removes a decommissioned gateway from /data, reporting how many
// destinations went. Its configuration history stays: see migration 00018.
func (r *GatewayRepo) Unpublish(ctx context.Context, gateway string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM gateway_destinations WHERE gateway = $1`, gateway)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_streams WHERE gateway = $1`, gateway); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
