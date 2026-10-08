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
