-- +goose NO TRANSACTION
-- +goose Up
-- The console's reads by workflow (#109): each workflow's last run, its recent
-- runs, its page. Every one filters runs by workflow and orders them by time,
-- and with no index each was a scan of the whole table -- the Workflows page
-- runs two of them per load and per keystroke of its search.
--
-- CONCURRENTLY, so a live installation keeps writing runs while it builds,
-- which is why this migration runs outside a transaction: the first in this
-- directory to.
--
-- DROPPED FIRST, and on purpose. A concurrent build that fails leaves an
-- invalid index of this name behind; goose has not recorded the migration, so
-- it runs it again, and a bare CREATE fails "already exists" on every try --
-- an installation stuck until somebody drops it by hand. IF NOT EXISTS would
-- be worse: it would keep the invalid one, silently.
DROP INDEX CONCURRENTLY IF EXISTS runs_workflow_criado_idx;
CREATE INDEX CONCURRENTLY runs_workflow_criado_idx ON runs (workflow_slug, criado_em DESC);

COMMENT ON INDEX runs_workflow_criado_idx IS
  'Leituras do console por workflow: ultimo run, runs recentes, a pagina do workflow.';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_workflow_criado_idx;
