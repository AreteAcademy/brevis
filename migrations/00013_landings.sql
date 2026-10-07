-- +goose Up
-- What each step declared it wrote: which destination, how much, and when.
--
-- A step says it on its stdout, as a marked line the runner already reads:
--
--     @brevis:{"type":"landed","target":"bigquery://acme-prod/bronze/clicks","rows":48213}
--
-- The Go SDK sends it after every successful load; a Python step calls
-- `landed()`; a dbt or shell step echoes it. This table is what the catalog on
-- /data is read from -- every destination the ecosystem writes, who writes it,
-- and whether it is late against the writer's schedule.
--
-- WHY NOT load_metrics. Its key is (run_id, node_id, map_index): one target per
-- step, which is true of an SDK pipeline and false of a Python step that writes
-- three tables. Widening that key would change what a trend row means; this is
-- a different question, so it is a different table, and load_metrics is
-- untouched.
--
-- NO foreign key to `runs`, for the reason 00012 gives: the catalog has to
-- outlive retention. A table loaded every day for a year and last pruned in
-- March is still a table that was loaded every day for a year, and a cascade
-- would make it disappear from /data on the morning of the purge.
--
-- TARGET is an identity, never an address: `postgres://analytics/public/orders`
-- with no host, port or user. The SDK's Locate() is the only thing that builds
-- one; the runner only checks its shape (internal/domain/catalog) and drops what
-- fails, so nothing here was repaired or inferred.
CREATE TABLE landings (
    run_id        UUID        NOT NULL,
    node_id       TEXT        NOT NULL,

    -- -1 for an unmapped step, matching task_runs.map_index.
    map_index     INT         NOT NULL DEFAULT -1,

    target        TEXT        NOT NULL,

    -- Denormalised, as in load_metrics: the writer is what the catalog groups
    -- by, and joining `runs` to find it is exactly what made the JSONB trend
    -- slow.
    workflow_slug TEXT        NOT NULL,

    -- When the step said it landed, or when the line arrived if it did not say.
    loaded_at     TIMESTAMPTZ NOT NULL,

    -- NULL means the step did not say, which is not zero: a step that does not
    -- count must not draw a table that emptied overnight.
    rows_written  BIGINT,
    bytes_written BIGINT,

    -- A row recovered from before targets existed: `target` then holds the old
    -- label (`postgres:landing.orders`), not a target. Set only by a backfill.
    legacy        BOOLEAN     NOT NULL DEFAULT false,

    -- A retry of the step overwrites its own numbers for a target it lands
    -- again, and keeps a target only the earlier attempt landed -- it did land.
    PRIMARY KEY (run_id, node_id, map_index, target)
);

-- The catalog's two questions: the latest loads of one destination, and the
-- latest load of each writer on each destination.
CREATE INDEX landings_target_idx ON landings (target, loaded_at DESC);
CREATE INDEX landings_writer_idx ON landings (workflow_slug, node_id, target, loaded_at DESC);

COMMENT ON TABLE landings IS
  'What each step declared it wrote. Read by /data. No FK to runs: outlives retention.';

-- Backfill: the loads that happened before steps could name their target.
--
-- What the history holds is the LABEL the load phase carried in
-- numeros.detail -- `postgres:landing.orders`, `bronze.clicks` -- and never a
-- target. Turning a label into a URI would mean guessing the project or the
-- database, which is the inference this catalog refuses, so the label goes in
-- `target` as it was written and the row is marked legacy. The screen shows it
-- as such, and the next load from an upgraded SDK lands beside it under its
-- real name.
--
-- The rule for "a finished load" is the one 00012 and LoadNumbersFrom share,
-- for the same reasons and with the same guard: the phase is found by NAME (two
-- map stages push `load` to index three), only `done` counts (half of a load
-- never happened), and a scalar in `etapas` must not abort the migration -- the
-- CASE is at the source, not in a WHERE the planner may evaluate late.
--
-- One difference from 00012: a retried step has a task_run per attempt, and
-- DISTINCT ON keeps the LAST attempt that finished its load, which is what
-- RecordLandings does going forward. 00012 lets ON CONFLICT pick, which is any.
--
-- `rows` is cast only when it is a JSON number, so a malformed value from some
-- old fetcher becomes NULL -- "did not say" -- instead of aborting the upgrade.
--
-- ON CONFLICT DO NOTHING never overwrites a real landing. There can be none on
-- a fresh upgrade, and the rule costs nothing if this is ever run again.
--
-- WHAT IT COSTS ON THE WAY IN, measured rather than assumed: 350,400
-- task_runs -- a year of hourly runs across forty workflows, 492 MB with
-- their phases and logs, wider rows than 00012's probe -- backfill in 10.8 s
-- cold, 8.1 s and 6.4 s warm, and leave 117 MB of landings. Inside the
-- migration transaction, so roughly eleven seconds added to that upgrade,
-- once, on top of 00012's own. Worth knowing before a deploy window; not worth
-- a background job and a screen that is wrong for the first hour.
INSERT INTO landings (
    run_id, node_id, map_index, target, workflow_slug,
    loaded_at, rows_written, legacy)
SELECT DISTINCT ON (t.run_id, t.node_id, t.map_index)
       t.run_id, t.node_id, t.map_index,
       l.carga->'numeros'->>'detail',
       r.workflow_slug,
       r.criado_em,
       CASE WHEN jsonb_typeof(l.carga->'numeros'->'rows') = 'number'
            THEN (l.carga->'numeros'->>'rows')::numeric::bigint END,
       true
FROM task_runs t
JOIN runs r ON r.id = t.run_id
JOIN LATERAL (
    SELECT e FROM jsonb_array_elements(
        CASE WHEN jsonb_typeof(t.etapas) = 'array' THEN t.etapas ELSE '[]'::jsonb END) e
    WHERE e->>'nome' = 'load' AND e->>'estado' = 'done'
    ORDER BY (e->>'indice')::int LIMIT 1
) l(carga) ON true
WHERE COALESCE(l.carga->'numeros'->>'detail', '') <> ''
ORDER BY t.run_id, t.node_id, t.map_index, t.attempt DESC
ON CONFLICT (run_id, node_id, map_index, target) DO NOTHING;

-- +goose Down
DROP TABLE landings;
