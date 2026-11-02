---
title: SQL
description: Plain .sql files become tables and views, in dependency order — as a workflow step or on their own.
group: Modelling in SQL
order: 21
slug: sql
---

No templating: a model is a file that stays valid SQL, and the order comes from reading the SQL itself. Postgres and BigQuery, under the same conformance suite.

![The SQL flow: brevis-sql reads raw.orders, a table the SDK or the Gateway landed. The model staging.stg_orders becomes a view over it, and marts.orders a table over that view; the order comes from reading the SQL. The same models build on Postgres or BigQuery.](/assets/flow-sql.svg)

## Six commands

| command | what it does |
|---|---|
| `brevis-sql compile` | parses every model and resolves every edge, **connecting to nothing** |
| `brevis-sql graph` | prints the inferred edges, so a wrong one is seen and not discovered |
| `brevis-sql build` | creates or replaces every model, in dependency order |
| `brevis-sql test` | runs every model's tests; each is a SELECT that must find nothing |
| `brevis-sql serve` | answers read-only previews and queries over HTTP, so a console needs no warehouse credential |
| `brevis-sql generation bump` | records that a table was rebuilt, so `brevis-sql-api`'s cached answers over it go stale — `build --generations-from` does it for every model it builds |

`--select orders+` narrows any of them to one model and everything downstream of it.


## `brevis-sql serve` — reading a warehouse without a credential

The console's Data and SQL screens show what landed and what is in it. Neither reaches a warehouse: they ask this service, which runs beside them and holds the credential the engine deliberately does not.

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

A ceiling is not a budget: `--max-bytes` bounds one query, and `--budget` bounds the sum per connection per hour — checked against the quote, so a refusal comes before the money is spent. It bounds only warehouses that report bytes, which is BigQuery and not Postgres; the boot banner says which posture is in force, including having none.

**Only what the file declares is reachable**, and by default nothing it does is a write: every statement is classified before it runs, a read that would scan more than `--max-bytes` is priced and refused before it costs anything, and the warehouse itself is asked to refuse writes. The audit line carries a hash of the statement and never the statement.

**It no longer needs a service account of its own.** `brevis-sql build` creates tables, so its role can write, and a service that refused any such role meant a second account to create, keep and rotate. The role is no longer the only thing holding the line: Postgres runs each statement inside a transaction opened `READ ONLY` — where `SELECT nextval('s')`, a write no classifier can see inside, comes back refused by Postgres itself — and BigQuery reports what kind of statement it is on the dry run that prices it, so a `CREATE TABLE` or an `EXPORT DATA` is refused on the warehouse's own verdict. A warehouse that can offer neither still refuses a credential that can write.

Outside `BREVIS_ENV=local` a token is required and the service will not start without one.

### A development warehouse somebody may change

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

It **names connections**. One service reaches several warehouses, and unlocking the one you develop against must not unlock the one beside it. On a named connection every layer stands down and this service is exactly as powerful as its credential — the row ceiling bounds a screen and the byte ceiling bounds money, and neither bounds what a statement changes.

A token is required wherever it is set, and `BREVIS_ENV=local` is not an exception. The boot banner names every unlocked connection, the console draws a banner above the editor, and `brevis_sql_writable_connection` appears on the metrics. **Leave it unset in production**; no manifest in this repository sets it.

Tokens can be **named**: `BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` declares a caller called `console`, and every audit line that bearer produces carries `"caller":"console"`. Identity is the credential rather than a header a caller fills in, so the log cannot say anything the holder of that token did not hold. A refused token leaves a line too, and never records what was presented.

## A model is a file that stays valid SQL

The configuration is a leading block comment, so the file opens in an editor
with highlighting, runs in a console by hand, and `psql -f` executes it.

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**No templating.** `models/<schema>/<name>.sql` is `<schema>.<name>`, and
dependencies come from reading the SQL rather than from a function call you
have to write. `brevis-sql graph` prints what was inferred; `depends_on:` in
the header is the explicit override for the rare miss.

## From compile to test

Two models: a view that cleans `raw.orders`, and a table that depends on it.

`models/staging/stg_orders.sql`:

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`:

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` and `graph` connect to nothing:

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

`build` and `test`, against a Postgres:

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

The real output, from the published image — compile and graph connect to nothing. Against a Postgres, build and test end with "2 models built" and "2 tests passed".

### When a test fails

With an order that has no customer in `raw.orders`, `build` passes and the `not_null` test does not. The failure names the model, the column, how many rows and **the query** — which is the thing you paste into a console and keep narrowing:

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

The process exits with code 1, and the workflow step fails with it.

## An incremental model and its lookback

`materialized: incremental` needs a `unique_key` and a `watermark`. Each build
reads only the rows past the greatest watermark already in the target and
MERGEs them by key. That filter is strict, so a row that ties the watermark but
commits after the build, or arrives late with an older timestamp, is not seen
until `--full-refresh`. An optional `lookback` fixes that:

```sql
/* brevis
materialized: incremental
unique_key: [order_id]
watermark: updated_at
lookback: 2h
*/
```

The value is a whole number and `s`, `m`, `h` or `d`; anything else is refused
with the model's name. The filter becomes `updated_at >= max - 2h`, and the
MERGE absorbs the repeated rows. The watermark must be a TIMESTAMP column when
a lookback is set. Without `lookback` the SQL is unchanged.

## Tests are SELECTs that return violating rows

`not_null`, `unique`, `accepted_values`, `relationships`. Zero rows is a pass, and there is nothing else to interpret.

## It says what it wrote

Every materialized model prints the `@brevis:` line the Brevis engine already
reads, so it appears on `/data` beside everything else (see `build`'s output
above). A view reports no row count rather than zero: it holds none, and a
zero there is indistinguishable from a table that really is empty.

## Two warehouses, one suite

PostgreSQL and BigQuery (`--dialect bigquery`), and both run the same suite of
tests — written while there was a single implementation, so the second could
not quietly diverge.

BigQuery authenticates with Application Default Credentials: on a workload
identity, that is the pod's own identity, and `--dsn-from` names the variable
holding the project id.

## 13 MB

Distroless, nonroot, no shell. The official BigQuery client is 31 MB and 526
packages in an empty binary, so this talks to BigQuery over its REST endpoint
instead — about 200 lines, with a test for each way it could be quietly
wrong. A gate in the repository refuses that client by name, so the decision
survives a routine dependency bump.

## Verified against dbt

jaffle-shop, built by this image on the same seed data dbt used:

|  | result |
|---|---|
| rows and md5 of every row, per model | **identical, 13 of 13** |
| columns and types, per model | **identical, 13 of 13** |
| 13 models, first build | 1.56 s |

## What it does not do

**No dbt compatibility**, and that is a decision rather than a gap. About a
third of a real dbt project's models use nothing beyond `ref`, `source` and
`config`; the rest use macros, and packages are templating by design. Half a
compatibility nobody can predict is worse than none.

## Where to go next

| if you want | go to |
|---|---|
| see where SQL sits among the four pieces | [Ecosystem](/docs/ecosystem/) |
| run brevis-sql as a scheduled step | [Core](/docs/core/) |
| land the data it will model | [Go SDK](/docs/sdk/) or [Ingestion](/docs/ingestion/) |
