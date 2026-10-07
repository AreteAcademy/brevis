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
	rows, err := r.pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (workflow_slug, node_id, target)
			       workflow_slug, node_id, target, loaded_at, rows_written, legacy
			FROM landings
			ORDER BY workflow_slug, node_id, target, loaded_at DESC
		)
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
		ORDER BY l.target, l.workflow_slug, l.node_id`)
	if err != nil {
		return nil, err
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
			return nil, err
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	targets := make([]string, len(entries))
	for i, e := range entries {
		targets[i] = e.Target
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
		return nil, err
	}
	defer recent.Close()
	for recent.Next() {
		var target string
		var rows *int64
		if err := recent.Scan(&target, &rows); err != nil {
			return nil, err
		}
		if i, ok := index[target]; ok {
			entries[i].Recent = append(entries[i].Recent, rows)
		}
	}
	return entries, recent.Err()
}

// kindOf is a target's scheme, or "" for a legacy label, which has none.
func kindOf(target string) string {
	scheme, _, ok := strings.Cut(target, "://")
	if !ok {
		return ""
	}
	return scheme
}
