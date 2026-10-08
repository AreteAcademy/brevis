-- +goose Up
-- What landed to start this run.
--
-- A COLUMN AND NOT A KEY IN `params`. `params` are the values a workflow
-- DECLARES and a trigger resolves against that declaration -- an undeclared key
-- there is refused, correctly, because a param nobody declared is a typo. The
-- triggering targets are not a parameter of the workflow; they are a fact about
-- why this run exists, which is what `trigger_type` and `logical_date` already
-- are.
--
-- TEXT[] and not JSONB: it is a list of strings with no shape of its own, and
-- the only question ever asked of it is "which ones". Querying it is not a goal
-- -- the catalog answers "what landed" and this answers "what did THIS run
-- think landed", which is a snapshot and deliberately not the same question.
--
-- NULL for every run no landing started, which is all of them until a workflow
-- declares a trigger. Not an empty array: absent and empty are the same thing
-- here, and NULL is the one that costs no bytes on a table that already holds
-- every run the engine ever created.
ALTER TABLE runs ADD COLUMN trigger_targets TEXT[];

COMMENT ON COLUMN runs.trigger_targets IS
  'The targets whose landing started this run. NULL unless trigger_type = landed.';

-- +goose Down
ALTER TABLE runs DROP COLUMN trigger_targets;
