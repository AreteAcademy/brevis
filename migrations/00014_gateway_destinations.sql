-- +goose Up
-- Where each published gateway's streams land, as `gateway describe` said.
--
-- The gateway shares nothing with the engine at runtime: no database, no API
-- call. It appears in the console through a deliberate act instead --
-- `gateway describe` in its deploy pipeline, `brevis gateway publish` with the
-- output -- which is the option docs/GATEWAY.md chose. So this table holds the
-- CONFIGURATION a gateway was published with, not its traffic: volume and
-- delivery stay on its /metrics, where they are live.
--
-- Rows are replaced per gateway on every publish, in one transaction: a stream
-- removed from the config disappears from /data on the next publish.
CREATE TABLE gateway_destinations (
    gateway      TEXT        NOT NULL,
    stream_path  TEXT        NOT NULL,

    -- sink, dead_letter, or archive (where an oversized event's body goes).
    role         TEXT        NOT NULL,

    -- The sink's type as the config spells it: pubsub, postgres, auto_table…
    kind         TEXT        NOT NULL,

    -- NULL when the gateway could not name the destination -- no locator in
    -- its binary, or a path relative to its working directory. `note` says
    -- which. Ending in /* when `routes`: auto_table's tables are learned from
    -- the events, so what can be published is where they land.
    target       TEXT,
    note         TEXT        NOT NULL DEFAULT '',
    routes       BOOLEAN     NOT NULL DEFAULT false,

    published_at TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (gateway, stream_path, role)
);

-- /data joins this to landings by target.
CREATE INDEX gateway_destinations_target_idx ON gateway_destinations (target);

-- +goose Down
DROP TABLE gateway_destinations;
