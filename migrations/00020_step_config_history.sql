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

-- What 00019 already recorded, as the read used to derive it.
INSERT INTO step_config_history (target, workflow_slug, node_id, config_hash, first_at, run_id)
SELECT target, workflow_slug, node_id, config_hash, loaded_at, run_id
FROM (
    SELECT target, workflow_slug, node_id, config_hash, loaded_at, run_id,
           lag(config_hash) OVER (PARTITION BY target, workflow_slug, node_id
                                  ORDER BY loaded_at, run_id, map_index) AS previous
    FROM landings
    WHERE config_hash IS NOT NULL
) x
WHERE previous IS DISTINCT FROM config_hash
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE step_config_history;
