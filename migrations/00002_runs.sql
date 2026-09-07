-- +goose Up
-- Runs, task_runs and PHASE 2's persistent queue.
--
-- Section 8 of the plan is explicit: "Never depend exclusively on an in-memory
-- channel for critical jobs". The queue lives in Postgres; the in-memory
-- dispatcher only consumes it.

CREATE TABLE runs (
    id               UUID PRIMARY KEY,
    workflow_slug    TEXT        NOT NULL,

    -- The idempotency key (section 29). The case it resolves: the scheduler
    -- creates the Run, dies before recording it, restarts and would try to
    -- create it again. With the unique, the second attempt collides instead of
    -- duplicating.
    idempotency_key  TEXT        NOT NULL UNIQUE,

    status           TEXT        NOT NULL,
    attempt          INT         NOT NULL DEFAULT 0,

    -- A snapshot of the graph at the instant of the trigger (section 22):
    -- editing the workflow later must not change the meaning of a past run.
    definicao        JSONB       NOT NULL,

    erro             TEXT        NOT NULL DEFAULT '',
    criado_em        TIMESTAMPTZ NOT NULL DEFAULT now(),
    iniciado_em      TIMESTAMPTZ,
    terminado_em     TIMESTAMPTZ
);

CREATE INDEX runs_status_idx ON runs (status) WHERE status NOT IN ('success', 'canceled');

CREATE TABLE task_runs (
    id            UUID PRIMARY KEY,
    run_id        UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    node_id       TEXT        NOT NULL,
    status        TEXT        NOT NULL,
    attempt       INT         NOT NULL DEFAULT 0,
    exit_code     INT,
    erro          TEXT        NOT NULL DEFAULT '',
    iniciado_em   TIMESTAMPTZ,
    terminado_em  TIMESTAMPTZ,

    UNIQUE (run_id, node_id, attempt)
);

CREATE TABLE queue_items (
    id            BIGSERIAL PRIMARY KEY,
    run_id        UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,

    -- Higher runs first. A retry goes in with a lower priority than new work so
    -- it does not monopolize the queue.
    prioridade    INT         NOT NULL DEFAULT 0,

    -- The retry's backoff: the item exists but is only visible after this
    -- instant.
    disponivel_em TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The ownership mark. NULL = free. Filled = some dispatcher claimed it.
    reivindicado_em  TIMESTAMPTZ,
    reivindicado_por TEXT,

    criado_em     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A run cannot be in the queue twice.
    UNIQUE (run_id)
);

-- The hot path's index: the claim looks for free, available items in priority
-- order. Partial so it does not index what has already been claimed.
CREATE INDEX queue_pendentes_idx
    ON queue_items (prioridade DESC, disponivel_em, id)
    WHERE reivindicado_em IS NULL;

-- +goose Down
DROP TABLE queue_items;
DROP TABLE task_runs;
DROP TABLE runs;
