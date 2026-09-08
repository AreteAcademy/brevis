-- +goose Up
-- What the ENGINE knows about a run, so no pipeline computes it again.
--
-- The value that matters is `adjusted_at`: the clock a step should read instead
-- of now(). A fetcher that reads the wall clock and subtracts its window is
-- right on a run that starts on time and forty minutes wrong on one the queue
-- delayed -- and those forty minutes belong to no run at all, because the next
-- slot reads its own now() too. Nothing fails; the data is simply missing.
--
-- STORED and not derived on read, and that is the reason for a column rather
-- than a view. `previous_error` is a fact about the instant this run began:
-- recomputing it tomorrow, after the previous run was retried and passed, would
-- answer a different question under the same name. So would `started_at` after
-- a retry.
--
-- Written when the run starts and rewritten on every attempt, because a retry
-- moves `started_at` and may change `previous_error` -- while `adjusted_at`
-- stays pinned to the slot, which is what keeps a retried run reading the same
-- window as the attempt that failed.
ALTER TABLE runs ADD COLUMN auto_params JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN runs.auto_params IS
  'Parametros automaticos da run: adjusted_at, interval, delay, previous_error. Snapshot do inicio da tentativa.';

-- +goose Down
ALTER TABLE runs DROP COLUMN auto_params;
