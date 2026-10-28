package postgres

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/domain/events"
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
// A stream that is new, or whose configuration changed, is in the events
// feed, in the same transaction, with actor as who published it -- the rule
// the history follows, so a republish that changes nothing records nothing.
//
// The manifest must already have been through catalog.ParseManifest.
func (r *GatewayRepo) Publish(ctx context.Context, m catalog.Manifest, at time.Time, actor string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// One publish of a gateway at a time. A deploy publishes it from a Job,
	// and two rollouts can overlap: interleaved, the delete-then-insert below
	// put a row back that the other had just put back, and failed on its
	// key. Transaction-scoped, so a failed publish never leaves it held.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, gatewayLock(m.Gateway)); err != nil {
		return err
	}
	// The streams published before this one, read before they are replaced:
	// what is new is what is not among them.
	rows, err := tx.Query(ctx, `SELECT stream_path FROM gateway_streams WHERE gateway = $1`, m.Gateway)
	if err != nil {
		return err
	}
	before, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_destinations WHERE gateway = $1`, m.Gateway); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_streams WHERE gateway = $1`, m.Gateway); err != nil {
		return err
	}
	for _, st := range m.Streams {
		changed, err := publishStream(ctx, tx, m.Gateway, st, at)
		if err != nil {
			return err
		}
		if isNew := !slices.Contains(before, st.Path); isNew || changed {
			if err := recordEvent(ctx, tx, events.StreamPublished, m.Gateway+st.Path, actor, map[string]any{
				"gateway": m.Gateway, "path": st.Path, "new": isNew,
			}); err != nil {
				return err
			}
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

// gatewayLock is the advisory lock key of one gateway's publish. Its own
// namespace, so it cannot share a key with a step's history (historyLock).
func gatewayLock(gateway string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("gateway-publish\x00"))
	_, _ = h.Write([]byte(gateway))
	return int64(h.Sum64())
}

// publishStream stores one stream's configuration and its current row, and
// says whether its configuration changed: whether the history gained a line.
func publishStream(ctx context.Context, tx pgx.Tx, gateway string, st catalog.ManifestStream, at time.Time) (bool, error) {
	changed := false
	hash, body := st.ConfigHash()
	var ref *string
	if hash != "" {
		ref = &hash
		if _, err := tx.Exec(ctx, `
			INSERT INTO writer_configs (hash, body, first_seen) VALUES ($1, $2, $3)
			ON CONFLICT (hash) DO NOTHING`, hash, body, at); err != nil {
			return false, err
		}
		// A line only when the configuration differs from the LAST one: going
		// back to an earlier configuration is a change too, and is recorded.
		tag, err := tx.Exec(ctx, `
			INSERT INTO gateway_config_history (gateway, stream_path, config_hash, published_at)
			SELECT $1, $2, $3, $4
			WHERE $3 IS DISTINCT FROM (
				SELECT config_hash FROM gateway_config_history
				WHERE gateway = $1 AND stream_path = $2
				ORDER BY published_at DESC LIMIT 1)
			ON CONFLICT DO NOTHING`, gateway, st.Path, hash, at)
		if err != nil {
			return false, err
		}
		changed = tag.RowsAffected() == 1
	}
	var notes []byte
	if len(st.Notes) > 0 {
		var err error
		if notes, err = json.Marshal(st.Notes); err != nil {
			return false, err
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO gateway_streams (gateway, stream_path, name, config_hash, notes, published_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, gateway, st.Path, st.Name, ref, notes, at)
	return changed, err
}

// Unpublish removes a decommissioned gateway from /data, reporting how many
// destinations went. Its configuration history stays: see migration 00018.
// Taking down a gateway that had streams is in the events feed.
func (r *GatewayRepo) Unpublish(ctx context.Context, gateway, actor string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM gateway_destinations WHERE gateway = $1`, gateway)
	if err != nil {
		return 0, err
	}
	streams, err := tx.Exec(ctx, `DELETE FROM gateway_streams WHERE gateway = $1`, gateway)
	if err != nil {
		return 0, err
	}
	if n := streams.RowsAffected(); n > 0 {
		if err := recordEvent(ctx, tx, events.GatewayUnpublished, gateway, actor, map[string]any{"streams": n}); err != nil {
			return 0, err
		}
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
