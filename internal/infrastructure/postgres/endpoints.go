package postgres

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
)

// EndpointRepo stores what brevis-sql-api files were published with.
type EndpointRepo struct{ pool *Pool }

func NewEndpointRepo(p *Pool) *EndpointRepo { return &EndpointRepo{pool: p} }

// PublishedEndpoint is one row: an endpoint as its API last published it.
type PublishedEndpoint struct {
	API         string
	Name        string
	Path        string
	Tenant      string
	Reads       []string
	Endpoint    catalog.Endpoint
	PublishedAt time.Time
}

// Publish replaces one API's endpoints with the manifest's, in one
// transaction, under a lock of that API's own -- the gateway's lesson: two
// overlapping rollouts interleaving delete-then-insert put back a row the
// other had just put back, and failed on its key. Other APIs are untouched.
//
// The manifest must already have been through catalog.ParseEndpoints.
func (r *EndpointRepo) Publish(ctx context.Context, m catalog.Endpoints, at time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, endpointsLock(m.Name)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM api_endpoints WHERE api = $1`, m.Name); err != nil {
		return err
	}
	for _, e := range m.Endpoints {
		config, err := json.Marshal(e)
		if err != nil {
			return err
		}
		reads := e.Reads
		if reads == nil {
			reads = []string{}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO api_endpoints (api, name, path, tenant, config, reads, published_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			m.Name, e.Name, e.Path, e.Tenant, config, reads, at); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// endpointsLock is the advisory lock key of one API's publish, in its own
// namespace beside the gateway's.
func endpointsLock(api string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("endpoints-publish\x00"))
	_, _ = h.Write([]byte(api))
	return int64(h.Sum64())
}

// Unpublish removes a decommissioned API, reporting how many endpoints went.
func (r *EndpointRepo) Unpublish(ctx context.Context, api string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM api_endpoints WHERE api = $1`, api)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const endpointColumns = `api, name, path, tenant, config, reads, published_at`

// List is every published endpoint, by API and path.
func (r *EndpointRepo) List(ctx context.Context) ([]PublishedEndpoint, error) {
	return r.query(ctx, `SELECT `+endpointColumns+` FROM api_endpoints ORDER BY api, path`)
}

// Reading is the endpoints whose SQL reads a target -- a model page's
// consumers beside its writers.
func (r *EndpointRepo) Reading(ctx context.Context, target string) ([]PublishedEndpoint, error) {
	return r.query(ctx, `SELECT `+endpointColumns+` FROM api_endpoints WHERE reads @> ARRAY[$1]::text[] ORDER BY api, path`, target)
}

// Get is one endpoint, and whether it is published.
func (r *EndpointRepo) Get(ctx context.Context, api, name string) (PublishedEndpoint, bool, error) {
	got, err := r.query(ctx, `SELECT `+endpointColumns+` FROM api_endpoints WHERE api = $1 AND name = $2`, api, name)
	if err != nil || len(got) == 0 {
		return PublishedEndpoint{}, false, err
	}
	return got[0], true, nil
}

func (r *EndpointRepo) query(ctx context.Context, sql string, args ...any) ([]PublishedEndpoint, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublishedEndpoint
	for rows.Next() {
		var e PublishedEndpoint
		var config []byte
		if err := rows.Scan(&e.API, &e.Name, &e.Path, &e.Tenant, &config, &e.Reads, &e.PublishedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(config, &e.Endpoint); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
