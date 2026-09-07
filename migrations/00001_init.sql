-- +goose Up
-- Phase 0 creates only the two entities that prove the end-to-end path.
-- Section 22 of the plan lists ten (workflow_versions, runs, task_runs,
-- queue_items, ...); they are born in the phases that use them — rule 2 forbids
-- anticipating, and a schema with no use case ages wrong.

CREATE TABLE projects (
    id          UUID PRIMARY KEY,
    slug        TEXT        NOT NULL UNIQUE,
    name        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflows (
    id          UUID PRIMARY KEY,
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    slug        TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The slug is unique WITHIN the project, not globally: two projects can
    -- each have a `daily_ingest` workflow without colliding.
    UNIQUE (project_id, slug)
);

-- +goose Down
DROP TABLE workflows;
DROP TABLE projects;
