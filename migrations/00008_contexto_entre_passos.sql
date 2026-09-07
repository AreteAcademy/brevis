-- +goose Up
-- What a step published, for the steps that depend on it.
--
-- It goes on task_runs and not on runs because this is where "did this step
-- succeed" already lives, and the two have to move together: a resumed run
-- skips a step that succeeded and still has to hand its output to the step
-- below. A separate table would let the two disagree.
--
-- NULL and '{}' are different: NULL is a step that published nothing, which is
-- the common case and legitimate.
ALTER TABLE task_runs ADD COLUMN saida JSONB;

-- +goose Down
ALTER TABLE task_runs DROP COLUMN saida;
