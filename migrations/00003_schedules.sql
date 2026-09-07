-- +goose Up
-- Schedules, and where each Run came from.
--
-- Section 37 separates the responsibilities: the scheduler CREATES runs, the
-- queue EXECUTES them. Which is why `schedules` holds nothing about execution —
-- no run status, no attempt counter.

-- The graph's definition starts living in the database (section 22: "Never
-- depend exclusively on the YAML file after the workflow has been published").
ALTER TABLE workflows ADD COLUMN definicao JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Why the Run exists (section 12). Without it there is no telling a backfill
-- from a scheduled run while investigating an incident.
ALTER TABLE runs ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'manual';

-- The logical slot this Run represents. Null for a manual trigger, which
-- belongs to no slot.
ALTER TABLE runs ADD COLUMN logical_date TIMESTAMPTZ;

CREATE TABLE schedules (
    id             UUID PRIMARY KEY,

    -- One schedule per workflow in this phase. Section 22 suggests N (a daily
    -- cron and a reconciliation one, for instance); when that is needed, the
    -- unique goes and a schedule name comes in.
    workflow_slug  TEXT        NOT NULL UNIQUE,

    cron           TEXT        NOT NULL,
    timezone       TEXT        NOT NULL DEFAULT 'UTC',
    catchup        BOOLEAN     NOT NULL DEFAULT false,
    ativo          BOOLEAN     NOT NULL DEFAULT true,

    -- The last slot ALREADY materialized into a Run. It is what stops the
    -- scheduler from recreating the same gap on every cycle.
    ultimo_slot    TIMESTAMPTZ,

    criado_em      TIMESTAMPTZ NOT NULL DEFAULT now(),
    atualizado_em  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX schedules_ativos_idx ON schedules (ativo) WHERE ativo;

-- +goose Down
DROP TABLE schedules;
ALTER TABLE runs DROP COLUMN logical_date;
ALTER TABLE runs DROP COLUMN trigger_type;
ALTER TABLE workflows DROP COLUMN definicao;
