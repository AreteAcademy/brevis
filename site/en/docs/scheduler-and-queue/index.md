# Scheduler and queue

> Two independent loops — who creates runs, who executes them, and why they are separate.

*https://brevis.sh/en/docs/scheduler-and-queue/ · brevis.sh docs (en)*

---

brevis.sh separates **creating** a run from **executing** it. Two loops run
together and depend on neither.

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## Why separate

A single loop that both schedules and executes has one bad property: when
execution jams, scheduling stops with it. The next twelve hours of slots simply
do not exist, and nobody notices until someone looks for data that never
arrived.

With the two apart:

- the **scheduler** can go down and the queue keeps draining what was created;
- the **queue** can go down and slots keep being materialised, to be executed
  when it returns;
- **reprocessing does not depend on the clock** — `backfill` creates past runs
  through the same path cron would use.

## The process

Both loops live in the same command:

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | default | |
|---|---|---|
| `--interval` | `10s` | interval between scheduler cycles |
| `--concurrency` | `5` | simultaneous **runs** |
| `--max-pods` | `5` | simultaneous **steps** in total |

`--concurrency` and `--max-pods` count different things, deliberately: five runs
with three parallel steps each would be fifteen pods if the only limit were on
runs.

:::note `serve` does not materialise schedules
The API process brings up the same scheduler, but **without the loop** — it is
there only to serve manual triggers from the interface, so the rule "the
scheduler creates runs" keeps a single owner. For schedules to fire, a
`brevis scheduler` must run alongside.
:::

## The definition snapshot

When the scheduler creates a run, it stores **the workflow definition inside the
run**. Execution reads that snapshot, not the YAML on disk.

The reason is concrete: between trigger and execution, someone may have edited
the file and deployed. Without the snapshot, a run created at 05:00 with one
definition would execute at 05:02 with another — and the log would have no way
of explaining the difference.

## Retries

A failed run is attempted **three times**, and the delay **doubles**: with the
defaults they land at **0s, 30s and 1m30s**.

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| flag | default | |
|---|---|---|
| `--max-attempts` | `3` | attempts per run, counting the first |
| `--retry-backoff` | `30s` | the first delay, doubled on each attempt after it |
| `--retry-backoff-max` | `1h` | ceiling for that delay |

The process prints the schedule at boot, so it never has to be worked out from
two numbers:

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**A rate-limited vendor wants longer.** These pipelines fail for one reason
above all others: a transient upstream — an API that answers 200 with "you have
reached your request limit", a warehouse quota blip. Those take about a minute
to clear, and a retry that finishes before then is a retry that never happened.
`--retry-backoff 1m` puts the attempts at 0s, 1m and 3m.
:::

The retry is per **run**, and it re-runs only the steps that failed — a
`dbt_build` that failed does not re-run the fetch above it that succeeded.

The attempt is persisted, not process state. Restarting the worker does not
erase what was already known about that run.

The **run's** attempt number goes into the pod name. Without it, a retry would
restart the run from zero, meet the previous attempt's pod under the same name
and hang in `Pending` forever.

## Backfill

Materialises past slots of a workflow already published:

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 run(s) de backfill enfileirados para daily (2026-01-01 a 2026-01-31)
  rode `brevis scheduler` para executa-los
```

It **enqueues, it does not execute.** Execution is the `scheduler`'s job — the
same separation again. `--to` includes the whole day.

## Started by a landing

A workflow can run because data arrived, instead of because the clock said so:

```yaml
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
```

Every step already declares what it wrote, on the pipe that carries the phases
— that is what fills `/data`. A workflow subscribes to one of those names and
starts when something lands on it.

A trailing `/*` subscribes to the whole dataset. It exists for `auto_table`,
which creates a table per route: the dataset is the only thing that can be
named for tables nobody declared. It matches `bronze/orders` and not
`bronze_raw/orders`.

### The window, and why ten landings are one run

`debounce` collapses a burst. Ten tables landing inside five minutes start
**one** run, not ten, and the run says which ones:

```
BREVIS_AUTO_LANDED=bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items
```

The exact list is also in `BREVIS_AUTO_PARAMS` as JSON, which is what to read
when a target could hold a comma.

The window is cut from **when the engine recorded the landing**, not from when
the step said it happened. A step that reports the instant its query began is
reporting something true and something old, and one skewed clock would
otherwise split a burst into two runs.

Leave `debounce` out and every landing starts a run.

### What it will not do

**A workflow is never started by its own write.** The engine drops a landing
whose writer is the subscribing workflow and logs why — without it, a workflow
that reads and writes the same table would trigger itself forever.

It is refused **when it fires** and not when the file is checked, and that is a
limit rather than an oversight: `brevis validate` reads files and no file
declares what a step writes. A warehouse target is decided by the connection,
not by the YAML. So `validate` passes a workflow that will be refused at
runtime, every time, with the reason in the log.

**The first cycle starts nothing.** A trigger begins counting from the moment
it goes live, like a schedule — otherwise publishing one would fire a run for
every landing in the catalog's history.

## Alerts

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

Without the webhook, the process warns at boot that failures will not be
reported. An installation that fails silently tends to be discovered by the
customer, not by the team.

`BREVIS_UI_URL` builds the run link inside the alert — without it, the alert
says what failed but makes the reader hunt for the run by hand.

## Next steps

- [Pod per step](/en/docs/pod-per-step/index.md) — where each step actually runs
- [CLI](/en/docs/cli/index.md) — every flag of `scheduler` and `backfill`
