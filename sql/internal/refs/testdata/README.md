# Corpus

30 queries, 15 per dialect, each with the references a model built on it
depends on, labelled by hand before any candidate ran.

## What counts as a reference

A relation the query READS, written as its path with quotes and backticks
removed and case kept (`"Raw"."Order Items"` → `Raw.Order Items`). One per
line, sorted.

Not a reference: a CTE name; a MERGE target (it is written, and a model
depending on its own output is the cycle the DAG must not have); a table
function or generated array (`generate_series`, `unnest`,
`generate_date_array`); a correlated array path (`e.items` after `FROM e,`);
an identifier inside a comment or a string.

A wildcard table and INFORMATION_SCHEMA are references, with the path as
written.

## Provenance

- postgres/01–07: dbt-labs/jaffle-shop, compiled by dbt-core 1.12.5 +
  dbt-postgres — real models, including CTEs named like the models they read
  (05) and a model that reads no table at all (04).
- postgres/08–15 and bigquery/01–15: written for this spike to cover the
  cases the spec lists. bigquery/01, 02 and 14 follow the GA4 BigQuery export
  schema (`events_*`, `_TABLE_SUFFIX`, `event_params`, `items`).

The spec named gitlab-data analytics as a source. Its models are Snowflake
SQL, which neither dialect here parses, so it is used only for the Jinja
census (Q3), not here. No transformations of the owner's were available.
