-- +goose Up
-- Since when a run has been going back to the queue because every agent of a
-- host was draining (#146).
--
-- Such a run spends no attempt: nothing in it ran. It goes back to the queue
-- for as long as this is under the dispatcher's ceiling, and past that it
-- fails like any other run. The FIRST instant, not the latest, or a run
-- refused every thirty seconds would never reach the ceiling. NULL whenever
-- the run is not waiting on a drain: spending an attempt or succeeding clears
-- it.
ALTER TABLE runs ADD COLUMN draining_since TIMESTAMPTZ;

COMMENT ON COLUMN runs.draining_since IS
  'Since when the run has been sent back to the queue for a draining host; NULL = not waiting on a drain.';

-- +goose Down
ALTER TABLE runs DROP COLUMN draining_since;
