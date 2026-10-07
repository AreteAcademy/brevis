# Landings

Which destination a step wrote, in any language.

```
@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/orders","rows":48213}
```

One line on stdout, after writing. The engine keeps it in the `landings` table:
which run and step, which destination, how many rows and bytes, and when. That
is the whole contract, and it is the same in every language. The console's
`/data` is drawn from it — see [`CATALOG.md`](CATALOG.md).

| | |
|---|---|
| Go SDK | nothing to do — a pipeline emits it after every successful load |
| Python | `from brevis import landed; landed("bigquery://…", rows=n)` |
| dbt, shell, anything | `echo` the line |

## A target is a name, not an address

| scheme | form |
|---|---|
| `bigquery` | `bigquery://{project}/{dataset}/{table}` |
| `postgres` | `postgres://{database}/{schema}/{table}` |
| `redshift` | `redshift://{database}/{schema}/{table}` |
| `mysql` | `mysql://{database}/{table}` |
| `s3`, `gs` | `s3://{bucket}/{prefix}/` — the prefix written under |
| `file` | `file:///{absolute dir}/` |
| `pubsub` | `pubsub://{project}/{topic}` |

**No host, no port, no user, no password.** A host moves with a failover, and a
DSN is the string most likely to carry a credential: keyed by either, one table
would split in two the day its database moved, and a password would end up in a
table everyone with the console can read. A target with `@`, a port, a query
string or a fragment is refused.

**The prefix, not the object.** A writer names each file with a timestamp, so
naming the file would make every run a new destination.

**One table, one name.** The SDK folds what the database folds: an unquoted
Postgres or Redshift identifier is lower-cased, a quoted one keeps its case, and
a BigQuery partition decorator (`clicks$20261007`) is dropped. A character that
would read as structure — a `/` inside a quoted identifier, a space — is
percent-encoded, so it stays one segment.

The shape is checked in three places — the SDK, the engine and the Python
library — and all three read the same cases:
[`sdk/testdata/targets.txt`](../sdk/testdata/targets.txt), copied into the
other two, with a test in each that fails when its copy drifts.

## The Go SDK

Every writer in `sdk/to` implements `sdk.Locator`, and its target is resolved
the way its load resolves the destination — set, then the environment, then the
default — without opening a connection:

| writer | where each part comes from |
|---|---|
| `bigquery.Table` | through the same `config()` the load uses: `Project` → `GOOGLE_PROJECT_ID`; `Dataset` → `BREVIS_SDK_DATASET` → `landing`; `Name` |
| `postgres.Table` | database from `DSN` (parsed, never dialled) or `Conn`; schema from a qualified `Name`, else the DSN's `search_path`, else `public` |
| `redshift.Table` | as Postgres, from `DSN` |
| `mysql.Table` | database from a qualified `Name`, else the DSN's; with only `DB`, `Name` must be qualified |
| `to.Files` | the directory `Path` names; a relative local path made absolute |
| `pubsub.Topic` | `Project`, `Name` |

A `search_path` set on the database role cannot be seen without a query, and
`Locate()` makes none. When that is how a database is set up, qualify the name.

The line is emitted once per successful load, after the `load` phase closes,
with `rows` always — zero included — and `bytes` when the driver counted them.
A failed load emits nothing. A writer of your own that does not implement
`Locator` keeps working and emits nothing.

Like the phases, it is emitted only under the engine. A fetcher run by hand
prints neither.

## Python, dbt and the shell

```python
from brevis import landed

landed("bigquery://acme-prod/silver/orders", rows=len(df))
landed("s3://acme-landing/vendors/")            # rows unknown: say nothing
```

`landed()` checks the shape before writing and raises `LandingError` naming
what a target looks like. It prints the line whether or not an engine is
listening; by hand, it reads as what it is.

dbt's `on-run-end` can emit one line per model it built; the hook is in
[`lib/python-context/README.md`](../lib/python-context/README.md#which-table-a-step-wrote).
Any other step echoes the line:

```bash
echo '@brevis:{"type":"landed","target":"postgres://analytics/public/orders","rows":1200}'
```

## The fields

| field | | |
|---|---|---|
| `type` | required | `"landed"` |
| `target` | required | as above |
| `rows` | optional | a whole number of zero or more |
| `bytes` | optional | the same |
| `at` | optional | RFC 3339. Default: when the line arrived. More than five minutes ahead of the engine's clock is not believed |

**Absent is not zero.** A step that does not count leaves `rows` out, and the
engine stores "did not say" — `NULL` — rather than a zero that would draw a
table emptying overnight. A step that wrote nothing says `0`.

**Several lines for one target add up** within one attempt: `rows` and `bytes`
are summed and the latest `at` kept, so a step that writes in batches can land
per batch.

## What the engine keeps, and what it drops

One row per run, step, mapped instance and target, written when the step ends
**whatever its exit code** — a step that wrote a table and then failed did
write it. A retry replaces its own numbers for a target it lands again, and
keeps a target only an earlier attempt landed.

Dropped, never repaired — a repaired target would be an inferred one:

- a target that fails the shape check, or a count that is not a whole number of
  zero or more;
- the 101st distinct target of one attempt and beyond. Targets already seen
  still add up.

Landings have their own budget, apart from the 60 marked lines a step may spend
on phases: a step that lands per table does not push its own phases off the
graph. What was dropped is logged once per step, naming the step.

`landings` has no foreign key to `runs`, like `load_metrics`, and `brevis prune`
leaves it alone: a table loaded every day for a year is still that, after the
runs that loaded it are purged.

## Rows from before targets existed

Migration 00013 recovers, from each run's phases, the finished loads that
happened before steps could name their target. The history holds only the label
the `load` phase carried — `postgres:landing.orders`, `bronze.clicks` — so that
is what is stored, with `legacy = true`. A label is never turned into a target:
that would mean guessing a project or a database.

## Mixed versions

Either side can be upgraded first. Both directions were run with real binaries,
a scheduled workflow and the local executor:

| fetcher | engine | what happens |
|---|---|---|
| an SDK that emits `landed` | one that predates it | the run succeeds and its phases and load trend are recorded as before; the line is consumed as an unknown marker — it does not reach the step's log, and it costs one of the 60 marked lines a step may spend on phases |
| an SDK that predates `landed` | one that reads it | the run succeeds and its phases and load trend are recorded; nothing lands, because nothing was said |

So a fleet upgrading its fetchers one at a time loses nothing: each table starts
landing on the first load of a fetcher that can name it.
