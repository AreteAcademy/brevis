-- +goose Up
-- The console's events feed: what somebody did to the installation, in the
-- order it happened. Runs are not here -- they have their own table, and the
-- feed reads them from it -- so this holds what no other table keeps a
-- history of: a workflow published or removed, a schedule paused or resumed,
-- a stream or an endpoint published or taken down.
--
-- Insert-only: nothing updates an event, and only prune deletes one. No
-- foreign key, for the reason runs have none to workflows: the event outlives
-- what it is about. `kind` is text and not an enum, so a new kind needs no
-- migration; the engine keeps the closed list in internal/domain/events.
CREATE TABLE events (
    id       BIGSERIAL   PRIMARY KEY,
    at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind     TEXT        NOT NULL,
    subject  TEXT        NOT NULL,
    -- Who did it: the signed-in user on the console, 'cli' from the command
    -- line, '' on a console with no login.
    actor    TEXT        NOT NULL DEFAULT '',
    -- Names, counts and flags only -- never a configuration or a credential.
    detail   JSONB       NOT NULL DEFAULT '{}'
);

CREATE INDEX events_at_idx ON events (at DESC, id DESC);

-- +goose Down
DROP TABLE events;
