-- +goose Up
-- Canceling a run from the console or the CLI (D3).
--
-- A run still waiting is canceled at once. A run a dispatcher holds can only
-- be asked: the request is written here, and the dispatcher, which reads the
-- requests of the runs it holds, stops the steps and writes `canceled`.
-- canceled_by is who asked first, for the events feed; '' is a console
-- without login.
ALTER TABLE runs
  ADD COLUMN cancel_requested_at TIMESTAMPTZ,
  ADD COLUMN canceled_by         TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN runs.cancel_requested_at IS
  'When someone first asked to cancel the run; NULL = nobody did.';
COMMENT ON COLUMN runs.canceled_by IS
  'Who first asked to cancel the run: a console user, cli, or empty.';

-- +goose Down
ALTER TABLE runs
  DROP COLUMN cancel_requested_at,
  DROP COLUMN canceled_by;
