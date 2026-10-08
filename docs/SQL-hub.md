# brevis-sql

Plain `.sql` files become tables and views, in dependency order, as an
ordinary step of a Brevis workflow — or on its own.

```
models/staging/stg_orders.sql  →  staging.stg_orders   (view)
models/marts/orders.sql        →  marts.orders         (table)
                                   ↑ the order is inferred from the SQL
```

```bash
docker run --rm \
  -v ./my-project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db \
  areteacademy/brevis-sql:0.1.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

## Four commands

| command | what it does |
|---|---|
| `compile` | parses every model and resolves every edge, **connecting to nothing** |
| `graph` | prints the inferred edges, so a wrong one is seen and not discovered |
| `build` | creates or replaces every model, in dependency order |
| `test` | runs every model's tests; each is a SELECT that must find nothing |

`--select orders+` narrows any of them to one model and everything
downstream of it.

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

## Two warehouses, one conformance suite

PostgreSQL and BigQuery, and both run the same suite of tests — written while
there was a single implementation, so the second could not quietly diverge.
Every claim it makes is about what the server did.

BigQuery authenticates with Application Default Credentials: on a workload
identity, that is the pod's own identity, and `--dsn-from` names the variable
holding the project id.

## Tests are SELECTs that return violating rows

`not_null`, `unique`, `accepted_values`, `relationships`. Zero rows is a
pass, and there is nothing else to interpret. A failure names the model, the
column, a row and **the query** — which is the thing you paste into a console
and keep narrowing.

```
FAIL  marts.orders   relationships   customer_id   1 row(s)  e.g. "c9"
      SELECT COUNT(*) FROM (SELECT c.customer_id FROM marts.orders c
        LEFT JOIN staging.stg_customers p ON c.customer_id = p.customer_id
       WHERE c.customer_id IS NOT NULL AND p.customer_id IS NULL) AS v
```

## It says what it wrote

Every materialized model prints the line the Brevis engine already reads, so
it appears on `/data` beside everything else:

```
@brevis:{"type":"landed","target":"postgres://analytics/marts/orders","rows":61948}
```

A view reports no row count rather than zero: it holds none, and a zero
there is indistinguishable from a table that really is empty.

## 13 MB

Distroless, nonroot, no shell. The official BigQuery client is 31 MB and 526
packages in an empty binary, so this talks to BigQuery over its REST endpoint
instead — about 200 lines, with a test for each way it could be quietly
wrong. A gate in the repository refuses that client by name, so the decision
survives a routine dependency bump.

## What it does not do

**No dbt compatibility**, and that is a decision rather than a gap. About a
third of a real dbt project's models use nothing beyond `ref`, `source` and
`config`; the rest use macros, and packages are templating by design. Half a
compatibility nobody can predict is worse than none.

**No incremental models yet** — every build is a full rebuild. That is the
next milestone.

## Verified against dbt

jaffle-shop, built by this image on the same seed data dbt used:

| | result |
|---|---|
| rows and md5 of every row, per model | **identical, 13 of 13** |
| columns and types, per model | **identical, 13 of 13** |
| 13 models, first build | 1.56 s |

## Documentation

Source, issues and the full reference:
<https://github.com/AreteAcademy/brevis>

Licence: MIT.
