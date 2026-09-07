-- +goose Up
-- Stores the stages an SDK step announces while it runs.
--
-- Until now an SDK step was a grey box that turned green. Between "started" and
-- "finished" there were forty minutes in which the screen could not tell
-- "downloading page 300 of 4,803" from "stuck on Redshift's handshake".
--
-- JSONB in a column, and not a table of its own: they are at most four records
-- of ~60 bytes per attempt, always read alongside the parent row and never
-- queried on their own. And task_runs is already keyed by (run_id, node_id,
-- attempt), so the stages sit per attempt with no new FK and no new join.
--
-- The writer applies a ceiling; see `tetoDeEtapas` in the SDK and the collector
-- in the runner.
ALTER TABLE task_runs ADD COLUMN etapas JSONB NOT NULL DEFAULT '[]'::jsonb;

-- The SDK version the step announced, empty for a step that is not an SDK one.
--
-- It comes from the binary itself (runtime/debug), not from the YAML: nobody
-- types it and nobody keeps it in sync, so the badge on the screen has no way to
-- lie.
ALTER TABLE task_runs ADD COLUMN sdk_versao TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE task_runs DROP COLUMN sdk_versao;
ALTER TABLE task_runs DROP COLUMN etapas;
