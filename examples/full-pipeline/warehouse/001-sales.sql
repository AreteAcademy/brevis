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
