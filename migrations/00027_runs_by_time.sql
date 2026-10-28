-- +goose NO TRANSACTION
-- +goose Up
-- The events feed reads runs across every workflow, newest first: by when
-- they started and by when they finished. The only index on runs by time is
-- per workflow (00024), so without these each page of the feed was a scan of
-- the whole table. Partial: a run that has not started, or not finished, is
-- not in that list. `id` breaks a tie, which is what the feed's cursor needs.
--
-- CONCURRENTLY and dropped first, for the reasons 00024 gives: a live
-- installation keeps writing runs while these build, and a failed build must
-- not leave the migration stuck on an invalid index.
DROP INDEX CONCURRENTLY IF EXISTS runs_started_idx;
CREATE INDEX CONCURRENTLY runs_started_idx ON runs (iniciado_em DESC, id DESC)
  WHERE iniciado_em IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS runs_finished_idx;
CREATE INDEX CONCURRENTLY runs_finished_idx ON runs (terminado_em DESC, id DESC)
  WHERE terminado_em IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_finished_idx;
DROP INDEX CONCURRENTLY IF EXISTS runs_started_idx;
