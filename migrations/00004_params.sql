-- +goose Up
-- Run parameters (section 12: a Run has to record WHY and WITH WHAT values it
-- ran).
--
-- A column of its own, and not inside `definicao`: the definition is the GRAPH's
-- snapshot, immutable; the params are that run's input. Mixing the two would
-- make two triggers of the same workflow have different snapshots with nothing
-- in the workflow having changed.
ALTER TABLE runs ADD COLUMN params JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE runs DROP COLUMN params;
