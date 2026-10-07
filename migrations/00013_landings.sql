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

-- +goose Down
DROP TABLE landings;
