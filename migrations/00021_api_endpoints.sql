-- +goose Up
-- What each published brevis-sql-api serves, as `brevis-sql-api describe`
-- said.
--
-- The API shares nothing with the engine at runtime -- no database, no call,
-- no route -- so, like a gateway, it appears in the console through a
-- deliberate act: `describe` in its deploy pipeline, `brevis endpoints
-- publish` with the output. This holds the CONFIGURATION an endpoint was
-- published with, never its traffic, which stays on the API's /metrics.
--
-- Rows are replaced per API on every publish, in one transaction: an
-- endpoint removed from the file disappears on the next publish.
CREATE TABLE api_endpoints (
    api          TEXT        NOT NULL,
    name         TEXT        NOT NULL,
    path         TEXT        NOT NULL,

    -- `key` when each request reads as its key's tenant, `none` when the
    -- file said the data is the same for every key.
    tenant       TEXT        NOT NULL,

    -- The endpoint's parameters, limits and marked notes, as the manifest
    -- carried them. No SQL: it is in the customer's repository.
    config       JSONB       NOT NULL,

    -- The catalog targets its SQL reads, in any branch. A model's page lists
    -- the endpoints over it from here.
    reads        TEXT[]      NOT NULL DEFAULT '{}',

    published_at TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (api, name)
);

-- "Which endpoints read this target": the model page's question.
CREATE INDEX api_endpoints_reads_idx ON api_endpoints USING GIN (reads);

-- +goose Down
DROP TABLE api_endpoints;
