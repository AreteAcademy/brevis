# Changelog — brevis-sql

`areteacademy/brevis-sql`, published from a `sql/v*` tag.

Its own version, on purpose, and for the reason the gateway's changelog
gives: this is a separate Go module with a separate maturity, and numbering
it with the engine would claim one it does not have. Versions cannot be
un-published; aligning two cadences later is easy, splitting one is not.

The engine's versions are in [`CHANGELOG-motor.md`](../CHANGELOG-motor.md),
the Go SDK's in [`CHANGELOG.md`](../CHANGELOG.md) and the gateway's in
[`gateway/CHANGELOG.md`](../gateway/CHANGELOG.md).

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the versions follow [SemVer](https://semver.org/).

---

## [0.3.0] — 2026-10-10

### Added: `brevis-sql serve` — a read-only window on a warehouse

```bash
brevis-sql serve --addr 127.0.0.1:8088 --connections connections.yaml
```

A second subcommand beside `compile`. It holds the warehouse credential that
the engine deliberately does not, and answers five endpoints:

    POST /v1/preview     the first rows of a destination
    POST /v1/query       a statement the caller wrote
    POST /v1/objects     what a connection holds
    POST /v1/columns     what one relation holds
    POST /v1/estimate    what a statement would scan, before it runs

**POST and never GET**, every one of them. A target in a URL is a target in a
proxy log, in a browser's history and in a `Referer` header; a statement is
worse. The method pattern also answers a GET with 405 for free.

**Preview and query are two endpoints and not one with a flag.** `/v1/preview`
takes a target and composes its own statement, so it cannot be handed SQL at
all — a property you can read off the route, rather than one you have to trust
a handler to preserve.

### Added: every limit is on this side

The console is a convenience; this is the line, and the tests send what
somebody with the token and `curl` would send.

- **Reads only.** A classifier refuses anything that is not a single read, and
  what its lexer cannot read to the end it refuses rather than passes.
- **The read-only role is asserted, not assumed.** Before any other limit
  applies, the connection is asked — without writing — whether its own
  credential could. A credential that can write makes every other limit on
  this surface decorative, and a role nobody checked is a role nobody has.
- **A row ceiling** bounds the screen. **A byte ceiling** bounds the money: a
  `LIMIT` does not reduce what BigQuery scans, so twenty rows off a petabyte
  table reads the petabyte. The preview is bounded by both.
- **An hourly budget per connection**, because four queries at a time, each
  under the ceiling, repeated forever, is unbounded. It is checked on the
  quote, so a refusal happens before the money is spent, and recorded on the
  bill, because the quote is not what gets paid.
- **A token, or the service refuses to exist** outside `BREVIS_ENV=local`,
  where it starts with a banner saying what that means.

### Added: the audit line says who asked

One line per request, whatever happened to it, carrying the caller, the
connection, the outcome, the rows and the bytes. **The statement is a
SHA-256 prefix and never the text** — it is what ties a price to the query
that followed it without putting anybody's SQL on disk. Counters sit beside
the audit line on a listener of its own, because a scrape endpoint on the port
that answers requests would publish every connection name to whoever can reach
it.

### Added: BigQuery and Postgres

A target may name either, and each scheme keeps its own name rules: a quoted
identifier is distinct on Postgres and two spellings of a column are two
columns on purpose, while BigQuery folds case. The Postgres reader asks the
SDK what `pgx` handed it rather than deciding for itself.

A warehouse that cannot do something optional is not an error. Postgres has no
`Estimator`, so it is not priced; a dialect that cannot list is not refused for
staying quiet. The caller is told which, in a sentence.

### Added: `/v1/estimate` — what a statement would scan, before it runs

A dry run creates no job and bills nothing. It returns strictly less than
`/v1/query` returns for the same statement, and a statement over the ceiling
is refused here with the same sentence it would get at Run — which is the only
moment at which refusing is worth anything.

**It is the one endpoint here that spends nothing, so it is the one with a
rate limit of its own.** The budget bounds every other loop on this service by
bounding what a loop costs; a free call is free to make forever, and on
BigQuery a dry run still counts against the project's API quota. 120 a minute
per caller, in a fixed window.

**A warehouse that cannot price says so, and is not guessed at.** `EXPLAIN`
was considered and refused: it returns a cost in planner units nobody is
billed in, and a number in the wrong unit under a line that says what a query
will process is worse than no number.

The figure is audited as `estimated` and not as `bytes`, because `bytes` is
what the `scanned` counter adds up and a dry run scanned nothing.

### Fixed: an open service stopped answering "does this dataset exist"

A dry run fails on a syntax error or a missing table, and that message is the
one thing the person who typed the SQL needs back. Without a token it is an
existence oracle: `SELECT * FROM payroll.x` comes back as *"Dataset
acme-prod:payroll was not found"* to anybody who can reach the port. The
warehouse's own words now reach a caller who authenticated, and nobody else.

### Fixed: "could not check" is not "checked and fine"

A capability probe that failed was being read as a pass. A check that cannot
fail is worse than no check, because it is counted as one.

### Upgrading

**Nothing changes for `brevis-sql compile`.** No flag was removed or renamed,
and the model syntax is unchanged.

**`serve` starts nothing by itself.** It is a subcommand somebody deploys
deliberately, and the decision is not a version bump: it holds a warehouse
credential, it is the only thing in this repository that does, and it should
be bound to a loopback address or sit behind something that authenticates.
`BREVIS_ENV` is required where it matters, and a service with no token outside
local refuses to start rather than opening quietly.

The engine reaches it through `BREVIS_SQL_SERVE_URL`; an engine older than
0.18.0 will not ask for `/v1/estimate`, and a console pointed at a `serve`
older than this one draws no cost line rather than an error.

---

## [0.2.0] — 2026-10-08

### Added: a model can process only what is new

```sql
/* brevis
materialized: incremental
unique_key: [order_id]
watermark: updated_at
*/
select * from bronze.orders
```

The first build creates the table. Every build after it takes only the rows
past the greatest watermark already there, deduplicates them by key keeping
the latest, and MERGEs. `--full-refresh` rebuilds from scratch and forgets
the watermark.

**Both fields are required and neither has a default**, because every default
here is wrong in a way nobody sees. No watermark would mean "take everything",
which rebuilds in full every night while reporting an incremental build: it
costs money and looks like it works. No unique key would mean a plain INSERT,
so a source that re-emits a corrected row grows a duplicate per run.

There is also nothing to fall back TO. Neither warehouse will run a MERGE
whose source holds two rows for one target row, which is exactly what a source
re-emitting a correction produces:

```
bigquery      UPDATE/MERGE must match at most one source row for each target row
postgres 17   MERGE command cannot affect row a second time
```

Both measured against real warehouses, which answers the one question the
spike behind #62 had to leave open.

**PostgreSQL 15 is the floor** for an incremental model, because `MERGE`
arrived there. `INSERT … ON CONFLICT DO UPDATE` reaches further back and
refuses without a unique index on the key — so supporting it would mean asking
for an index this tool never creates and does not maintain. Views and tables
are unaffected.

### Added: the new watermark reaches the steps below

```
@brevis:{"type":"context","value":{"silver.orders":"2026-03-11T04:00:00Z"}}
```

On the pipe that already carries the landings, keyed by model, and read after
the build rather than before — the number worth publishing is where this run
got to, not where the last one stopped.

### Added: `--full-refresh`

On `build`, and it works with `--select` so one model can be rebuilt without
the project. It does nothing to a view or a table, which are rebuilt anyway.

### Known limits

**No lookback window.** A row corrected with a watermark behind the one
already in the target is invisible to an incremental build. `--full-refresh`
is the way through; a declared lookback is not in this version.

**A model that changes shape** — a new column in the SELECT list — is not
migrated. The MERGE names the target's columns, so the warehouse refuses it
and says which name it does not have, rather than landing data in the wrong
place. `--full-refresh` rebuilds it with the new shape.

---

## [0.1.0] — 2026-10-08

### Added: plain `.sql` files become tables and views, in dependency order

```
brevis-sql compile              # parse everything, connect to nothing
brevis-sql graph                # the inferred edges, so a wrong one is seen
brevis-sql build --dsn-from BREVIS_SQL_DSN
brevis-sql test  --dsn-from BREVIS_SQL_DSN
```

**No templating.** A model is a `.sql` file that stays valid SQL: its
configuration is a leading `/* brevis … */` comment, so the file opens in an
editor with highlighting, runs in a console by hand, and `psql -f` executes
it. A model you cannot paste into a client is a model you debug twice.

**Dependencies are inferred from the SQL and then SHOWN.** `brevis-sql
graph` prints what was inferred, so a wrong edge is seen rather than
discovered at two in the morning; `depends_on:` in the header is the
explicit override for the rare miss. The extractor reads 99.3% of 1,543
fresh Postgres statements and 26/26 fresh BigQuery, with **zero non-stdlib
packages** — the spike measured three real parsers at 4, 70 and 642.

**Two dialects, one conformance suite.** Postgres and BigQuery, and the
suite was written while there was a single implementation — which is the
whole point: an interface with one implementation is a description of that
implementation, and the second one added later agrees wherever the author
happened to look. Every claim it makes is about what the server did.

**Tests are SELECTs that return violating rows.** `not_null`, `unique`,
`accepted_values`, `relationships`. Zero rows is a pass, and a failure names
the model, the column, a row and the query — which is the thing you paste
into a console and keep narrowing.

**Every materialized model appears on `/data`**, through the `@brevis:`
line the engine already reads. One contract for every language, and the
engine imports no part of this module.

**13 MB, distroless, nonroot.** The official BigQuery client is 31 MB and
526 packages in an empty main, over the whole image budget before a line of
this is written; the REST endpoint that replaces it is about 200 lines with
a test for each way it could be quietly wrong.
`.github/scripts/sql-weight.sh` refuses that client by name, so the
decision survives a `go get`.

### Not doing

**No dbt compatibility.** The spike measured a production dbt project at
28% of models free of templating beyond `ref`/`source`/`config`, and 68%
using macros. "SQL without templates" holds for a project written for
brevis-sql and for about half of a real dbt project once SQL functions are
first-class; it does not hold for dbt packages, 40% of which introspect the
warehouse. Saying so is better than a half-compatibility nobody can predict.

[#62](https://github.com/AreteAcademy/brevis/issues/62)
