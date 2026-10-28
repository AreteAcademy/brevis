-- +goose Up
-- A failed run did not record when it ended, so the events feed and the
-- insights' durations left every failure out. The engine records it now; the
-- runs that had already failed get the end of their last step, or when they
-- were created if no step ever ran.
UPDATE runs r
SET terminado_em = COALESCE(
      (SELECT max(t.terminado_em) FROM task_runs t WHERE t.run_id = r.id),
      r.criado_em)
WHERE r.status = 'failed' AND r.terminado_em IS NULL;

-- +goose Down
-- Nothing: a filled-in end cannot be told from a recorded one, and either is
-- right for a run that failed.
SELECT 1;
