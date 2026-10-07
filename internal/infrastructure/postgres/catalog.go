package postgres

import (
	"context"
	"strings"
	"time"
)

// CatalogEntry is one destination on /data: what wrote it and what it received.
type CatalogEntry struct {
	// Target is the destination's identity -- or, for a legacy row, the label
	// its load carried before targets existed.
	Target string

	// Kind is the target's scheme (`bigquery`, `postgres`, `s3`…); empty for a
	// legacy label, which has none.
	Kind string

	// Legacy marks a label recovered by migration 00013, never a target.
	Legacy bool

	Writers []CatalogWriter

	// Recent is the rows of the destination's last loads, oldest first, across
	// every writer -- what the sparkline draws. Nil where the step did not say.
	Recent []*int64
}

// CatalogWriter is one workflow step landing on a destination, with what
// freshness needs to judge it.
type CatalogWriter struct {
	Workflow, Node string

	LastLoaded time.Time
	LastRows   *int64

	// The writer's schedule. HasSchedule is false for a workflow that only runs
	// by hand.
	HasSchedule    bool
	Cron, Timezone string
	Active         bool

	// Lag is the p90 of how long after its slot this writer's data landed, over
	// its last 20 landings. Zero when no slot is left to measure -- the runs
	// were purged -- which leaves the 10-minute floor as the grace.
	Lag time.Duration

	// RunInFlight is the id of a run of this workflow that is queued, running
	// or retrying, if there is one.
	RunInFlight *string

	// Gateway is set when the writer is a published gateway stream rather than
	// a workflow step; Workflow and Node are then empty, and nothing above is
	// measured -- a gateway's traffic is on its /metrics.
	Gateway *GatewayWriter
}

// GatewayWriter is a gateway stream as its published manifest describes it.
type GatewayWriter struct {
	Name, Stream, Role, Kind, Note string
	Routes                         bool
	PublishedAt                    time.Time
}

// Catalog lists every destination the ecosystem has landed on.
//
// Two round trips and no more, whatever the number of destinations:
//
//  1. The latest landing of each writer, with its schedule, its lag and any
//     run in flight. DISTINCT ON is ordered as landings_writer_idx is, so the
//     latest per writer is read off the index. The lag reads the writer's last
//     20 landings from that same index and each one's slot from runs by
//     primary key: `runs` has no index by workflow, and a p90 of run durations
//     per workflow would scan it -- which is why the grace is measured on the
//     landing (decided with the spec's owner, 2026-10-07).
//  2. The last 30 loads of each destination, through landings_target_idx with
//     a LIMIT per target.
//
// Status is NOT computed here. This knows rows and times; what "late" means is
// the domain's (catalog.Freshness), and the page applies it.
func (r *ReadRepo) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	entries, index, err := r.catalogWriters(ctx, nil)
	if err != nil {
		return nil, err
	}
	if entries, err = r.gatewayWriters(ctx, nil, entries, index); err != nil || len(entries) == 0 {
		return nil, err
	}
	return entries, r.recentLoads(ctx, entries, index)
}

// gatewayWriters adds what published gateways write, joined by target: a
// table a step and a gateway both write is one entry with both writers. A
// destination the manifest could not name becomes an entry of its own with an
// empty Target -- listed with its note, never dropped.
func (r *ReadRepo) gatewayWriters(ctx context.Context, target *string,
	entries []CatalogEntry, index map[string]int) ([]CatalogEntry, error) {

	query := `SELECT gateway, stream_path, role, kind, target, note, routes, published_at
		FROM gateway_destinations ORDER BY gateway, stream_path, role`
	args := []any{}
	if target != nil {
		query = `SELECT gateway, stream_path, role, kind, target, note, routes, published_at
			FROM gateway_destinations WHERE target = $1 ORDER BY gateway, stream_path, role`
		args = append(args, *target)
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g GatewayWriter
		var tgt *string
		if err := rows.Scan(&g.Name, &g.Stream, &g.Role, &g.Kind, &tgt, &g.Note, &g.Routes, &g.PublishedAt); err != nil {
			return nil, err
		}
		w := CatalogWriter{Gateway: &g}
		if tgt == nil {
			entries = append(entries, CatalogEntry{Writers: []CatalogWriter{w}})
			continue
		}
		i, ok := index[*tgt]
		if !ok {
			i = len(entries)
			index[*tgt] = i
			entries = append(entries, CatalogEntry{Target: *tgt, Kind: kindOf(*tgt)})
		}
		entries[i].Writers = append(entries[i].Writers, w)
	}
	return entries, rows.Err()
}

// latestEveryWriter is each writer's latest landing, by SKIPPING through
// landings_writer_idx rather than reading it.
//
// The obvious form, DISTINCT ON over (workflow_slug, node_id, target), reads
// every entry of the index to keep one per writer. Measured on a probe of
// 1,051,220 landings -- a year of hourly runs across forty workflows, 500
// destinations, 452 MB -- that was 837 ms of the page's 990 ms (p50), against
// a target of 150 ms. Postgres 17 has no skip scan, so the recursive CTE does
// it by hand: one index probe to the next writer, one to its latest landing,
// 500 of each. The same probe: 29 ms.
//
// Measured after the change, 20 calls each on that probe, warm:
//
//	Catalog                        p50 45 ms   p95 54 ms
//	/data, query + build + render  p50 47 ms   p95 57 ms
//	CatalogTarget                  p50  2 ms   p95  2 ms
const latestEveryWriter = `
		WITH RECURSIVE writers AS (
			(SELECT workflow_slug, node_id, target FROM landings
			 ORDER BY workflow_slug, node_id, target LIMIT 1)
			UNION ALL
			SELECT nx.workflow_slug, nx.node_id, nx.target FROM writers w
			CROSS JOIN LATERAL (
				SELECT workflow_slug, node_id, target FROM landings
				WHERE (workflow_slug, node_id, target) > (w.workflow_slug, w.node_id, w.target)
				ORDER BY workflow_slug, node_id, target LIMIT 1
			) nx
		),
		latest AS (
			SELECT x.* FROM writers w
			CROSS JOIN LATERAL (
				SELECT workflow_slug, node_id, target, loaded_at, rows_written, legacy FROM landings
				WHERE workflow_slug = w.workflow_slug AND node_id = w.node_id AND target = w.target
				ORDER BY loaded_at DESC LIMIT 1
			) x
		)`

// latestOneTarget is the same for one destination, through landings_target_idx.
// Its own text rather than `$1 IS NULL OR target = $1`, which the planner
// cannot use an index for: on the probe that form cost the target page 195 ms.
const latestOneTarget = `
		WITH latest AS (
			SELECT DISTINCT ON (workflow_slug, node_id)
			       workflow_slug, node_id, target, loaded_at, rows_written, legacy
			FROM landings
			WHERE target = $1
			ORDER BY workflow_slug, node_id, loaded_at DESC
		)`

// catalogWriters is the first round trip: every writer, or one target's.
func (r *ReadRepo) catalogWriters(ctx context.Context, target *string) ([]CatalogEntry, map[string]int, error) {
	latest, args := latestEveryWriter, []any{}
	if target != nil {
		latest, args = latestOneTarget, []any{*target}
	}
	rows, err := r.pool.Query(ctx, latest+`
		SELECT l.target, l.legacy, l.workflow_slug, l.node_id, l.loaded_at, l.rows_written,
		       s.cron IS NOT NULL, COALESCE(s.cron, ''), COALESCE(s.timezone, ''), COALESCE(s.ativo, false),
		       COALESCE(lag.p90, 0),
		       inflight.id::text
		FROM latest l
		LEFT JOIN schedules s ON s.workflow_slug = l.workflow_slug
		LEFT JOIN LATERAL (
			SELECT percentile_cont(0.9) WITHIN GROUP (
			         ORDER BY GREATEST(0, EXTRACT(EPOCH FROM x.loaded_at - COALESCE(ru.logical_date, ru.criado_em)))
			       ) AS p90
			FROM (
				SELECT run_id, loaded_at FROM landings x
				WHERE x.workflow_slug = l.workflow_slug AND x.node_id = l.node_id AND x.target = l.target
				ORDER BY x.loaded_at DESC
				LIMIT 20
			) x
			JOIN runs ru ON ru.id = x.run_id
		) lag ON true
		LEFT JOIN LATERAL (
			SELECT id FROM runs
			WHERE workflow_slug = l.workflow_slug AND status IN ('queued', 'running', 'retrying')
			ORDER BY criado_em DESC
			LIMIT 1
		) inflight ON true
		ORDER BY l.target, l.workflow_slug, l.node_id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var entries []CatalogEntry
	index := map[string]int{}
	for rows.Next() {
		var (
			target string
			legacy bool
			w      CatalogWriter
			lagSec float64
		)
		if err := rows.Scan(&target, &legacy, &w.Workflow, &w.Node, &w.LastLoaded, &w.LastRows,
			&w.HasSchedule, &w.Cron, &w.Timezone, &w.Active, &lagSec, &w.RunInFlight); err != nil {
			return nil, nil, err
		}
		w.Lag = time.Duration(lagSec * float64(time.Second)).Round(time.Second)
		i, ok := index[target]
		if !ok {
			i = len(entries)
			index[target] = i
			entries = append(entries, CatalogEntry{Target: target, Kind: kindOf(target), Legacy: legacy})
		}
		entries[i].Writers = append(entries[i].Writers, w)
	}
	return entries, index, rows.Err()
}

// recentLoads is the second round trip: the rows of each target's last loads.
func (r *ReadRepo) recentLoads(ctx context.Context, entries []CatalogEntry, index map[string]int) error {
	targets := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Target != "" {
			targets = append(targets, e.Target)
		}
	}
	recent, err := r.pool.Query(ctx, `
		SELECT t.target, x.rows_written
		FROM unnest($1::text[]) AS t(target)
		CROSS JOIN LATERAL (
			SELECT rows_written, loaded_at FROM landings
			WHERE target = t.target
			ORDER BY loaded_at DESC
			LIMIT 30
		) x
		ORDER BY t.target, x.loaded_at`, targets)
	if err != nil {
		return err
	}
	defer recent.Close()
	for recent.Next() {
		var target string
		var rows *int64
		if err := recent.Scan(&target, &rows); err != nil {
			return err
		}
		if i, ok := index[target]; ok {
			entries[i].Recent = append(entries[i].Recent, rows)
		}
	}
	return recent.Err()
}

// TargetDetail is one destination for its own page: the entry as the list
// shows it, and its last loads one by one.
type TargetDetail struct {
	CatalogEntry
	Loads []TargetLoad
}

// TargetLoad is one landing on the destination, newest first.
type TargetLoad struct {
	RunID          string
	Workflow, Node string
	LoadedAt       time.Time
	Rows, Bytes    *int64
}

// CatalogTarget reads one destination, or nil when nothing ever landed there.
// A legacy label is looked up as stored, so its page opens like any other.
func (r *ReadRepo) CatalogTarget(ctx context.Context, target string) (*TargetDetail, error) {
	entries, index, err := r.catalogWriters(ctx, &target)
	if err != nil {
		return nil, err
	}
	if entries, err = r.gatewayWriters(ctx, &target, entries, index); err != nil || len(entries) == 0 {
		return nil, err
	}
	if err := r.recentLoads(ctx, entries, index); err != nil {
		return nil, err
	}
	d := &TargetDetail{CatalogEntry: entries[0]}

	rows, err := r.pool.Query(ctx, `
		SELECT run_id::text, workflow_slug, node_id, loaded_at, rows_written, bytes_written
		FROM landings
		WHERE target = $1
		ORDER BY loaded_at DESC
		LIMIT 30`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l TargetLoad
		if err := rows.Scan(&l.RunID, &l.Workflow, &l.Node, &l.LoadedAt, &l.Rows, &l.Bytes); err != nil {
			return nil, err
		}
		d.Loads = append(d.Loads, l)
	}
	return d, rows.Err()
}

// kindOf is a target's scheme, or "" for a legacy label, which has none.
func kindOf(target string) string {
	scheme, _, ok := strings.Cut(target, "://")
	if !ok {
		return ""
	}
	return scheme
}
