-- +goose Up
-- How far the scheduler has read the landings.
--
-- ONE ROW, not one per workflow. The poll asks "what was recorded since X" and
-- then matches each landing against every subscription, so a second workflow
-- subscribing to the same target costs nothing and a cursor per workflow would
-- be N reads of one answer.
--
-- IT IS PLANTED AT now() ON THE FIRST CYCLE and never at the epoch, which is
-- the rule `schedules.ultimo_slot` learned the hard way: a marker left NULL
-- makes the first cycle materialise from the beginning of time, and the fix
-- there is written up in scheduler.go after eighteen workflows went without an
-- automatic run. A trigger that fired once for every landing in the table's
-- history would be the same mistake with a louder blast radius.
--
-- A SINGLETON ENFORCED BY THE PRIMARY KEY rather than by convention. `id`
-- is always true, so a second row cannot be inserted -- which is cheaper than
-- a LIMIT 1 that silently picks one of two rows that disagree.
CREATE TABLE landing_cursor (
    id          BOOLEAN     PRIMARY KEY DEFAULT true CHECK (id),
    recorded_at TIMESTAMPTZ NOT NULL
);

COMMENT ON TABLE landing_cursor IS
  'How far the scheduler has read landings. One row. Planted at now(), never at the epoch.';

-- The subscriptions themselves are read from `workflows.definicao`, which is
-- where a workflow''s trigger lives -- it is part of the GRAPH the file
-- declares, not a schedule, and a column of its own would be a second place
-- for it to disagree with the document that was published.
--
-- The index is partial: almost no workflow has a trigger, and an index over
-- all of them would be mostly empty pages.
--
-- THE PREDICATE IS A TYPE CHECK AND NOT A LENGTH, because a workflow with no
-- trigger stores `"OnLanded": null` -- Go marshals a nil slice as JSON null,
-- not as an absent key and not as `[]`. `jsonb_array_length` on a scalar
-- RAISES, and a partial index whose predicate raises refuses the INSERT: the
-- first version of this refused to publish any workflow without a trigger,
-- which is all of them.
--
-- COALESCE does not help: it answers SQL NULL, and this is a JSON null, which
-- is a value. Same trap 00013 names for `etapas`.
CREATE INDEX workflows_triggered_idx ON workflows ((definicao -> 'Trigger' -> 'OnLanded'))
    WHERE jsonb_typeof(definicao -> 'Trigger' -> 'OnLanded') = 'array';

-- +goose Down
DROP INDEX workflows_triggered_idx;
DROP TABLE landing_cursor;
