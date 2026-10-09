-- +goose Up
-- When the ENGINE wrote the landing down, as opposed to when the step says it
-- happened.
--
-- WHY `loaded_at` CANNOT BE POLLED. It is the step's own clock: the `at` field
-- of the `@brevis:{"type":"landed",...}` line, taken as the step reported it.
-- The runner clamps it five minutes into the future and does not clamp it
-- against the past at all -- deliberately, because a step that reports the
-- instant its query began is reporting something true, and a landing dated an
-- hour ago is an ordinary thing.
--
-- So a poll asking "what landed since my cursor" on `loaded_at` would never
-- see a row a skewed or honest clock dated before it. The table also has no
-- sequence: its key is (run_id, node_id, map_index, target).
--
-- THIS COLUMN IS NOT A SAFE CURSOR EITHER, and saying so here is the point. A
-- transaction that began before the cursor can commit after it, and its row is
-- then behind a cursor already advanced. The repair is not a better column: it
-- is polling a window with OVERLAP and letting the run's idempotency key --
-- `slug:trigger:slot`, UNIQUE since 00002 -- make a landing seen twice free.
-- A second sighting composes the same key and creates nothing.
--
-- BACKFILLED TO `loaded_at`, which is the only value available and is never
-- read as a cursor. The trigger's cursor starts where the feature goes live,
-- exactly as a schedule plants `now` on its first cycle rather than firing for
-- every slot since the epoch -- the bug written up in scheduler.go that left
-- eighteen workflows without one automatic run.
-- WHAT IT COSTS ON THE WAY IN, measured rather than assumed, on the same shape
-- 00013 used -- 350,400 landings, a year of hourly loads across forty
-- workflows, 165 MB:
--
--     ADD COLUMN NOT NULL DEFAULT now()      7.5 ms
--     UPDATE ... SET recorded_at = loaded_at  7.4 s
--     CREATE INDEX                            231 ms
--
-- The ADD COLUMN does not rewrite the table: Postgres stores the default as a
-- missing value and fills it on read. The UPDATE does rewrite every row, and
-- leaves the table at 269 MB until it is vacuumed -- worth knowing before a
-- deploy window on a busy catalog, and not worth a background job and a
-- screen that is wrong for the first hour.
--
-- THE INDEX IS BUILT LAST, and that ordering is measured too: with the index
-- already there the same UPDATE takes 10.8 s instead of 7.4, because every
-- rewritten row maintains it for an answer no query asks until the first
-- cycle runs.
ALTER TABLE landings ADD COLUMN recorded_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- `loaded_at` and not now(): a row already here was recorded at some point in
-- the past, and stamping them all with the deploy's instant would put a year
-- of history inside one second -- which a window poll would then read as a
-- year of landings arriving at once.
UPDATE landings SET recorded_at = loaded_at;

-- The poll's question -- everything written since X -- which neither existing
-- index answers: both lead with `target` or `workflow_slug`.
CREATE INDEX landings_recorded_idx ON landings (recorded_at);

COMMENT ON COLUMN landings.recorded_at IS
  'When the engine wrote this down. loaded_at is the step''s clock and cannot be polled.';

-- +goose Down
DROP INDEX landings_recorded_idx;
ALTER TABLE landings DROP COLUMN recorded_at;
