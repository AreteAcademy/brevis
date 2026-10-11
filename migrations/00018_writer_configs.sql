-- +goose Up
-- How each writer is configured, and how that changed.
--
-- /data showed WHERE a writer lands; this is HOW. A gateway publishes its
-- streams' settings (`gateway describe`, manifest version 2), and the SDK will
-- describe its pipelines the same way, through the marker it already prints.
--
-- CONTENT-ADDRESSED. A configuration is stored once under the hash of its
-- body, whatever the number of publishes or runs pointing at it: a pipeline
-- that runs every minute with the same settings is one row, not 1,440 a day.
-- The engine computes the hash (catalog.ManifestStream.ConfigHash); a key a
-- client chooses is a key a client can collide.
--
-- NO CREDENTIAL REACHES IT. The writer keeps them out by construction, and the
-- engine refuses a description that carries one (catalog.FindSecret) rather
-- than storing it redacted. Every console login reads /data.
CREATE TABLE writer_configs (
    hash       TEXT        PRIMARY KEY,

    -- The settings, as an ordered list of {path, value, default}. The order is
    -- the writer's own -- the config file's, for a gateway -- which is the
    -- order somebody reads them in.
    body       JSONB       NOT NULL,

    first_seen TIMESTAMPTZ NOT NULL
);

-- A gateway's streams as last published: the configuration each one points at,
-- and the file's comments.
--
-- Beside gateway_destinations and not a column of it: a stream has one
-- configuration and up to three destinations, and the notes would otherwise be
-- stored three times. Replaced per gateway on every publish, in the same
-- transaction as its destinations.
CREATE TABLE gateway_streams (
    gateway      TEXT        NOT NULL,
    stream_path  TEXT        NOT NULL,
    name         TEXT        NOT NULL DEFAULT '',

    -- NULL for a stream published by a version-1 manifest, which described
    -- where it lands and nothing else.
    config_hash  TEXT        REFERENCES writer_configs (hash),

    -- The comments, by setting path; '' is the stream's own. NOT part of the
    -- hash: a comment reworded is the same configuration, so it replaces the
    -- old words here and makes no new version (decided with the owner,
    -- 2026-10-11).
    notes        JSONB,

    published_at TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (gateway, stream_path)
);

-- Every configuration a stream has had, oldest first, one line per CHANGE: a
-- publish with the settings unchanged adds nothing.
--
-- Unpublishing a gateway leaves this alone. Taking it off /data is a fact
-- about today; what it was configured with until then is the record an audit
-- asks for, and it outlives the gateway the way landings outlive runs.
CREATE TABLE gateway_config_history (
    gateway      TEXT        NOT NULL,
    stream_path  TEXT        NOT NULL,
    config_hash  TEXT        NOT NULL REFERENCES writer_configs (hash),
    published_at TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (gateway, stream_path, published_at)
);

-- +goose Down
DROP TABLE gateway_config_history;
DROP TABLE gateway_streams;
DROP TABLE writer_configs;
