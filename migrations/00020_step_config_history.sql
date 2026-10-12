-- +goose Up
-- Every configuration a step has written a destination with, one line per
-- CHANGE -- what gateway_config_history is for a gateway's streams.
--
-- 00019 read a step's history off its landings with a window over all of
-- them, and that was the mistake a review found: `landings` outlives
-- retention, so a step landing every minute gives a destination half a
-- million landings a year, and the destination's page sorted every one of
-- them to show twenty. This is written in the same transaction as the
-- landings (RecordLandings), only when the hash differs from the step's last
-- line for that target, and the page reads the top twenty off its key.
CREATE TABLE step_config_history (
    target        TEXT        NOT NULL,
    workflow_slug TEXT        NOT NULL,
    node_id       TEXT        NOT NULL,

    config_hash   TEXT        NOT NULL REFERENCES writer_configs (hash),

    -- When it was first written with: the landing's own time.
    first_at      TIMESTAMPTZ NOT NULL,

    -- The run that first wrote with it. No foreign key, for landings'
    -- reason: the line outlives the run.
    run_id        UUID        NOT NULL,

    -- Target first: the page asks "what has written THIS destination", and
    -- RecordLandings asks for one writer's last line on one target.
    PRIMARY KEY (target, workflow_slug, node_id, first_at)
);

-- What RecordLandings asks for each landing of a mapped step: the instance's
-- own previous landing on the target. Without map_index the index above it
-- (landings_writer_idx) walks every instance's landings to find one, N per
-- instance, N squared per run.
CREATE INDEX landings_instance_idx ON landings (workflow_slug, node_id, target, map_index, loaded_at DESC);

-- What 00019 already recorded, by the rule RecordLandings writes with: a
-- change is an instance's configuration differing from its own previous
-- landing, and it is a line unless it repeats the step's line before it.
-- Compared with the step's previous landing instead, a mapped step whose
-- instances are configured apart would be a, b, c again for every run.
INSERT INTO step_config_history (target, workflow_slug, node_id, config_hash, first_at, run_id)
SELECT target, workflow_slug, node_id, config_hash, loaded_at, run_id
FROM (
    SELECT target, workflow_slug, node_id, config_hash, loaded_at, run_id,
           lag(config_hash) OVER (PARTITION BY target, workflow_slug, node_id
                                  ORDER BY loaded_at, run_id, map_index) AS previous_line
    FROM (
        SELECT target, workflow_slug, node_id, map_index, config_hash, loaded_at, run_id,
               lag(config_hash) OVER (PARTITION BY target, workflow_slug, node_id, map_index
                                      ORDER BY loaded_at, run_id) AS instance_before
        FROM landings
    ) l
    WHERE config_hash IS NOT NULL
      AND instance_before IS DISTINCT FROM config_hash
) x
WHERE previous_line IS DISTINCT FROM config_hash
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE step_config_history;
DROP INDEX IF EXISTS landings_instance_idx;
