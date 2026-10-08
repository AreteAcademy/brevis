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

## [0.1.0] — unreleased

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
