---
title: SQL
description: Plain .sql files become tables and views, in dependency order — as a workflow step or on their own.
group: Modelling in SQL
order: 21
slug: sql
---

No templating: a model is a file that stays valid SQL, and the order comes from reading the SQL itself. Postgres and BigQuery, under the same conformance suite.

![The SQL flow: brevis-sql reads raw.orders, a table the SDK or the Gateway landed. The model staging.stg_orders becomes a view over it, and marts.orders a table over that view; the order comes from reading the SQL. The same models build on Postgres or BigQuery.](/assets/flow-sql.svg)

## Five commands

| command | what it does |
|---|---|
| `brevis-sql compile` | parses every model and resolves every edge, **connecting to nothing** |
| `brevis-sql graph` | prints the inferred edges, so a wrong one is seen and not discovered |
| `brevis-sql build` | creates or replaces every model, in dependency order |
| `brevis-sql test` | runs every model's tests; each is a SELECT that must find nothing |
| `brevis-sql serve` | answers read-only previews and queries over HTTP, so a console needs no warehouse credential |

`--select orders+` narrows any of them to one model and everything downstream of it.


## `brevis-sql serve` — reading a warehouse without a credential

The console's Data and SQL screens show what landed and what is in it. Neither reaches a warehouse: they ask this service, which runs beside them and holds the credential the engine deliberately does not.

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

A ceiling is not a budget: `--max-bytes` bounds one query, and `--budget` bounds the sum per connection per hour — checked against the quote, so a refusal comes before the money is spent. It bounds only warehouses that report bytes, which is BigQuery and not Postgres; the boot banner says which posture is in force, including having none.

**Only what the file declares is reachable**, and nothing it does is a write: every statement is classified before it runs, a read that would scan more than `--max-bytes` is priced and refused before it costs anything, and the credential itself is asserted to be read-only at first use. The audit line carries a hash of the statement and never the statement.

Outside `BREVIS_ENV=local` a `BREVIS_SQL_SERVE_TOKEN` is required and the service will not start without one.

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
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.1.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.1.0 \
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
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.1.0 \
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
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.1.0 \
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
