# The catalog

`/data` in the console: every destination Brevis writes, who writes it, when
it last received data, and whether that is late.

It is read from what steps declare they wrote — the `landed` line, see
[`LANDINGS.md`](LANDINGS.md). The engine opens no connection to any warehouse to
draw it: a destination is on this page because a step said it wrote there.

## What it lists, and what it does not

| listed | not listed |
|---|---|
| every table an SDK pipeline loaded | anything written by a tool outside Brevis |
| every destination a step declared with `landed` (Python, dbt, shell) | the gateway's sinks |
| loads from before targets existed, as **legacy** labels | |

The page says the same in one sentence under its title, so nobody reads the
list as complete.

## Late, by the writer's own schedule

A table three hours old is stale if it loads hourly and fine if it loads daily,
and only the writer's cron knows which. So a writer — a `workflow · step` — is
judged by how many of **its own** slots went by since its last landing:

```
grace  = max(10 min, lag)
none missed → on time      one → late      two or more → stale
```

A slot counts as missed once it is past its grace. **Lag** is how long after its
slot this writer's data usually lands: the 90th percentile of
`loaded_at − slot` over its last 20 landings, where the slot is the run's
logical date (its creation, for a manual run). Queue, retries and the step's
own duration are all in it. A writer that always lands 40 minutes after its
slot gets 40 minutes; the 10-minute floor keeps a load ending at `:03` from
flickering between late and on time.

The schedule is read the way the scheduler reads it, in the schedule's
timezone, so a daylight-saving change moves the slot exactly as it moves the
run.

| status | when |
|---|---|
| **on time** | no slot missed |
| **late** | one slot missed |
| **stale** | two or more |
| **paused** | the workflow's schedule is paused — a choice, never stale |
| **no schedule** | the workflow runs by hand, or its cron cannot be read |

A destination takes its **best** writer's status: data arriving from any writer
is data arriving. The list puts stale first, then late, on time, paused and no
schedule; within a status, the most recently loaded first.

A late writer whose workflow has a run queued, running or retrying links to it:
it may be about to fix itself.

## Filters

`?status=attention` (late or stale), `?status=<status>`, `?kind=<scheme>`
(`bigquery`, `postgres`, `s3`…), `?legacy=1`. They combine, they live in the
URL so a filtered page can be sent, and a value the page does not know is
ignored rather than turned into an empty page. The summary above the table
always counts every destination.

## One destination

`/data/target?u=<target>` — a query parameter, because a path would have its
`//` cleaned away. Each writer with its schedule, its own status and one
sentence on why, in its schedule's timezone; the last 30 loads as bars, each
opening its run; the slots missed since the last load drawn dashed, at most
six. A load that did not say how many rows it wrote is drawn hollow — never as
zero.

## Legacy rows

Migration 00013 recovers, from each run's recorded phases, the loads that
happened before steps named their target. History holds only the label the load
carried — `postgres:landing.orders`, `bronze.clicks` — so that is what the page
shows, marked legacy. It is never turned into a target, which would mean
guessing a project or a database. Once a fetcher on an SDK that names its
target loads again, the same table appears beside it under its real name.

## Cost

Two queries for the whole list, whatever the number of destinations, both
through `landings`' own indexes. On a probe of a million landings across 500
destinations the page renders in under 60 ms at the 95th percentile; the
measurement is in `internal/infrastructure/postgres/catalog.go`.
