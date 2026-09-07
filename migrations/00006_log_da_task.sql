-- +goose Up
-- Stores each step's output.
--
-- Until now the log lived only in the pod, and the pod is deleted when the step
-- ends. A pipeline that failed at 4am left "failed" on the screen and nothing
-- else: the dbt output that explained why had already gone with the container.
-- It was the most expensive gap to operate around in dev.
--
-- TEXT, and not a file on disk or a bucket: the real volume is small (a dbt run
-- with 60 nodes yields ~25 KB of text), Postgres compresses long TEXT in TOAST,
-- and keeping it here keeps the log next to the state it explains — with no
-- second system to query, expire and permission.
--
-- The writer applies a ceiling and marks what it cut; see `janela` in the
-- runner.
ALTER TABLE task_runs ADD COLUMN log TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE task_runs DROP COLUMN log;
