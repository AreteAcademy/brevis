package postgres

import (
	"context"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
)

// GatewayRepo stores what gateways were published with.
type GatewayRepo struct{ pool *Pool }

func NewGatewayRepo(p *Pool) *GatewayRepo { return &GatewayRepo{pool: p} }

// Publish replaces one gateway's destinations with the manifest's, in one
// transaction: a stream removed from the config is gone after this, and a
// failure leaves the previous publish whole rather than half of either.
// Other gateways' rows are not touched.
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
	for _, st := range m.Streams {
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

// Unpublish removes a decommissioned gateway, reporting how many rows went.
func (r *GatewayRepo) Unpublish(ctx context.Context, gateway string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM gateway_destinations WHERE gateway = $1`, gateway)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
