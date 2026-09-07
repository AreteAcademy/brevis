---
title: CLI
description: The engine's ten subcommands, with flags, arguments and what each one requires.
group: Reference
order: 7
slug: cli
---

The engine is one binary with subcommands, not several binaries: API, scheduler
and workers are roles of the same system, and one binary keeps one version, one
image and one build path.

```
brevis [command] [flags]
```

| command | database | credentials outside `local` | role |
|---|---|---|---|
| [`serve`](#serve) | **yes** | **required** | API + interface |
| [`scheduler`](#scheduler) | **yes** | **required** | both loops |
| [`migrate`](#migrate) | **yes** | **required** | schema |
| [`publish`](#publish) | **yes** | **required** | stores workflow and schedule |
| [`backfill`](#backfill) | **yes** | **required** | reprocesses a range |
| [`run`](#run) | no | — | runs now, on the instance itself |
| [`validate`](#validate) | no | — | validates workflow YAML |
| [`brand`](#brand) | no | — | validates branding YAML |
| [`hash`](#hash) | no | — | generates the password hash |
| [`version`](#version) | no | — | version, commit, build |

The first five load configuration, which **fails at boot** without
`BREVIS_DATABASE_URL`. The other five do not — which is why `validate` works in
CI, where there is no Postgres.

Those same five inherit the credentials rule: with `BREVIS_ENV` other than
`local`, coming up without the three authentication variables is a **boot
error**.

---

## serve

Brings up the HTTP API and the interface. No flags — everything comes from the
environment.

```bash
brevis serve
```

Shuts down on `SIGINT`/`SIGTERM` gracefully, so a deploy does not cut requests
in flight. This process **does not materialise schedules**: that needs a
`brevis scheduler` alongside.

`CMD` of the `api` image (distroless, no shell).

---

## scheduler

Both loops: the scheduler materialises schedules into runs, the dispatcher pulls
from the queue and executes.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | type | default | |
|---|---|---|---|
| `--interval` | duration | `10s` | interval between cycles |
| `--concurrency` | int | `5` | simultaneous **runs** |
| `--max-pods` | int | `5` | simultaneous **steps** in total |

`CMD` of the `worker` image (alpine with a shell, because `run:` steps need
one).

---

## alert

Delivers the alerts the scheduler recorded. A third process beside `serve` and
`scheduler`.

```bash
brevis alert --interval 1s --max-attempts 6
```

| flag | type | default | |
|---|---|---|---|
| `--interval` | duration | `1s` | interval between delivery cycles |
| `--max-attempts` | int | `6` | tries before an alert is recorded as undelivered |

**It exists because of where an alert is written, not because delivery deserves
a process.** The scheduler records the alert in the same transaction as the
failure that justifies it — either the run is out of attempts and the alert
exists, or neither happened. Something then has to drain that table, and it has
to survive both a Slack outage and its own restart. Before this, a failing
webhook meant a log line and an alert that was simply gone.

It refuses to start with no channel configured (`BREVIS_SLACK_WEBHOOK`). A
delivery process with nowhere to deliver drains the outbox into "undelivered"
as fast as it fills, and those rows look exactly like Slack rejecting them.

An alert it gives up on is **kept**: "raised, not delivered, 4 attempts, 403
from Slack" is the row that matters, because it is the case where somebody is
waiting for a message that is not coming.

`CMD` of the `worker` image.

---

## report

Sends the periodic summary of what ran.

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| flag | type | default | |
|---|---|---|---|
| `--window` | duration | `168h` | how far back to look |
| `--dry-run` | bool | `false` | print instead of sending |

A **command**, not a loop, and that is the difference between a report and an
alert: it runs from a CronJob, so there is no new loop, no new state and no
leader election — the cluster already has a scheduler. `--dry-run` is how
somebody comes to trust a weekly message, by reading what it would have said.

It does **not** go through the alerts outbox. The two share a delivery channel
and nothing else: losing an alert is an outage nobody hears about, and missing
one week's summary is next week's summary.

The message carries runs, successes, failures, the pipelines that failed most,
the ones that took longest, and the rows and bytes the SDK reported. It does
**not** carry CPU or memory, and does not print zero for them either — the
engine does not collect those, and the message says where they do live.

An empty window says so rather than reporting 100%. A success rate out of
nothing is the most reassuring number a report can print and the least true one:
an empty week usually means the scheduler was down.

`deployments/kubernetes/report.yaml` runs it weekly.

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

Takes exactly one argument. Migrations are embedded in the binary and applied by
their own subcommand — **`serve` never alters the schema**.

---

## validate

Validates one or more workflow files. Takes a file **or a directory**.

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

It touches no database and starts no server, so it runs in the editor and in CI.
It exits non-zero and counts the failures.

---

## run

Runs a workflow **now, on the instance itself**: no queue, no database, no
scheduler.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| flag | type | default | |
|---|---|---|---|
| `--param` | repeatable | — | `key=value` for a declared parameter |
| `--workdir` | string | the file's directory | working directory for steps |
| `--retries` | int | `1` | attempts per step (`1` = no retry) |
| `--timeout` | duration | `0` | timeout per step (`0` = no limit) |

`--param` without `=` is an **error**, not a warning: `--param load_full`,
forgetting the value, would run with the default — and the operator would
believe the value was applied.

:::warning Three limits
`run` only operates with `BREVIS_ENV=local`; a step with `image:` runs on the
instance itself and says so; and there is no Go task registry, so an `action:`
for an unregistered task fails naming the ones available.
:::

---

## publish

Stores workflows and their schedules in the database. Takes a file or a
directory.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| flag | type | default | |
|---|---|---|---|
| `--project` | string | `default` | project slug |
| `--prune` | bool | `false` | removes from the project the workflows absent from the list |

With `--prune`, the history of removed workflows is preserved.

---

## backfill

Materialises past slots of a workflow already published. It **enqueues, it does
not execute.**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| flag | | |
|---|---|---|
| `--from` | **required** | start date, `YYYY-MM-DD` |
| `--to` | **required** | end date, `YYYY-MM-DD` |
| `--param` | | applies to **every** slot in the range |

`--to` includes the whole day: internally the end becomes `23:59:59` of that
date.

---

## brand

Validates a branding file without starting the server.

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orquestração
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

One behavioural difference from boot: **a missing file is an error**. In
`serve`, absence means "use the default identity"; someone who asked to validate
a path expects to be told it does not exist.

---

## hash

Generates the `BREVIS_AUTH_SENHA_HASH` value.

```bash
brevis hash
```

The password is read from the terminal **without echo**, never from an argument:
an argument shows up in any process's `ps` and stays in shell history. It
refuses passwords shorter than 12 characters.

The hash goes alone to **stdout** and the labels to **stderr**, so
`brevis hash > hash.txt` stores only what matters.

---

## version

```bash
brevis version
```

```
brevis 0.3.0
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

Built directly with `go build`, it prints `brevis dev` with no commit and no
date — and telling that apart from a release artefact matters when someone
reports odd behaviour.
