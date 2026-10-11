-- +goose Up
-- Which configuration wrote each landing.
--
-- The Go SDK describes its pipeline once per attempt -- source, stages, target,
-- the `pipeline` marker -- and every landing of that attempt points at the
-- description here. A writer's history on /data is then the sequence of
-- distinct hashes across its landings: no table of its own, and it outlives
-- retention the way landings already do (00013).
--
-- NULL for a step that did not describe itself: Python, dbt, a shell. Those
-- land exactly as before, and the page says the step did not describe itself
-- rather than inventing a configuration for it.
ALTER TABLE landings ADD COLUMN config_hash TEXT REFERENCES writer_configs (hash);

-- +goose Down
ALTER TABLE landings DROP COLUMN config_hash;
