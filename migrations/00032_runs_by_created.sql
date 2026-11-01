-- +goose NO TRANSACTION
-- +goose Up
-- The Runs list and the Overview's latest runs read every workflow's runs,
-- newest created first. The only index on criado_em is per workflow (00024),
-- so without a workflow filter each page was a scan of the whole table and a
-- top-N sort. `id` breaks a tie, as in 00027.
--
-- CONCURRENTLY and dropped first, for the reasons 00024 gives.
DROP INDEX CONCURRENTLY IF EXISTS runs_created_idx;
CREATE INDEX CONCURRENTLY runs_created_idx ON runs (criado_em DESC, id DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_created_idx;
