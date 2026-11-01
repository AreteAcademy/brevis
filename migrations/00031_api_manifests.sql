-- +goose Up
-- An API's manifest beside its endpoints (screen-endpoints): what
-- `brevis endpoints publish` reads from the file and used to drop -- the
-- connection the API reads through, its SQL dialect, and its cache. The
-- console shows the cache's ttl and, for the command that clears it (D5),
-- the dialect and the NAME of the variable the cache's address is read from.
-- Never a credential: the manifest names variables, and the parser refuses
-- one holding something shaped like what it names.
CREATE TABLE api_manifests (
    api          TEXT        PRIMARY KEY,
    connection   TEXT        NOT NULL,
    dialect      TEXT        NOT NULL,
    cache        JSONB,
    published_at TIMESTAMPTZ NOT NULL
);

COMMENT ON COLUMN api_manifests.cache IS
  'The manifest''s cache: type, ttl, addr_from (a variable''s name), max_entry; NULL = no cache.';

-- +goose Down
DROP TABLE api_manifests;
