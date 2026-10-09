-- The warehouse's own schema, and it is DDL somebody can read.
--
-- NOT CREATED BY A WRITER, deliberately, and both writers agree. The
-- gateway's Postgres sink does not create tables -- its own configuration
-- says why: "a service that creates tables turns a typo here into a second
-- table nobody is reading" -- and the SDK's `CreateTable` is off by default
-- for the same reason. So the shape lives here, beside the workflow that
-- fills it, where a column can be read before it is relied on.
--
-- IDEMPOTENT, AND APPLIED ON EVERY `make up-data` rather than only by
-- Postgres's init hook. That hook runs once, on an EMPTY volume: edit this
-- file with a warehouse already created and nothing would happen, which is
-- the kind of silence that costs an afternoon.

CREATE SCHEMA IF NOT EXISTS sales;

-- What `daily_sales · load_warehouse` lands, one row per CSV line.
--
-- The columns are exactly what the step DECLARES, in its own order. The SDK
-- refuses a declared column the transform did not deliver and a field the
-- declaration does not list -- "dropping data in silence is the worst way to
-- fail" -- so this table and that list have to agree, and when they do not,
-- the run says which column.
CREATE TABLE IF NOT EXISTS sales.daily (
    partition TEXT        NOT NULL,
    sku       TEXT        NOT NULL,
    quantity  INTEGER     NOT NULL,
    price     NUMERIC     NOT NULL,
    loaded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- What the gateway's `orders` stream lands, one row per event.
--
-- THE SHAPE WAS ASKED FOR, NOT DEDUCED. The sink resolves the column list
-- from this table intersected with what the batch carries, and refuses a
-- mismatch before touching the server:
--
--   the rows carry column(s) order_id, placed_at, quantity, sku, total,
--   which sales.events does not have. They would be silently dropped, so the
--   load stops here: add the column to the table, or remove the field in
--   Transform
--
-- That message is the specification. Reading it beat deducing the shape from
-- four files, and the event that produced it went to the dead letter rather
-- than being lost.
--
-- `ingestion_id` IS UNIQUE because the stream declares `write: merge`. The
-- gateway refuses to guess between append and merge -- "appending a
-- redelivery into a table somebody counts and merging into a log that wanted
-- every arrival are both wrong, and only the table's owner knows which it
-- is" -- and orders are counted, so a redelivery must not become a second
-- row. The UNIQUE is what makes that true in the database rather than in a
-- hope.
--
-- NULLABLE except where an event cannot be without it: a field one event
-- omits is written as NULL, and a NOT NULL here would turn one odd event
-- into a batch nobody lands.
CREATE TABLE IF NOT EXISTS sales.events (
    ingestion_id TEXT        NOT NULL UNIQUE,
    order_id     TEXT        NOT NULL,
    placed_at    TIMESTAMPTZ,
    sku          TEXT,
    quantity     INTEGER,
    total        NUMERIC,
    loaded_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
