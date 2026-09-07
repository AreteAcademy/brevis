---
title: Workflows
description: The complete YAML format — steps, dependencies, images, resources and schedule.
group: Concepts
order: 4
slug: workflows
---

A workflow is a YAML file. It declares **what to run**, **in what order** and
**with what runtime** — and nothing beyond that.

## Minimum structure

```yaml
name: hello
steps:
  - id: only
    run: echo done
```

`name` identifies the workflow in the database and in the interface. `steps` is
the list of steps, each with a unique `id` and a `run`.

## Order: chain or dag

```yaml
type: chain   # order is the file's order
type: dag     # order comes from depends_on
```

`chain` is sugar: the parser turns the sequence into edges, and **the engine
only knows DAGs**. Use `dag` when the order is not linear:

```yaml
type: dag

steps:
  - id: prepare
    run: ./prepare.sh

  # Siblings: they depend on the same step, so they run in parallel.
  - id: extract
    run: ./extract.sh
    depends_on: [prepare]

  - id: validate
    run: ./validate.sh
    depends_on: [prepare]

  - id: publish
    run: ./publish.sh
    depends_on: [extract, validate]
```

The runner walks the graph **by levels**: everything within a level runs in
parallel, and the next level starts only when the previous one closes entirely.

:::warning A failure stops the whole level
The runner stops at the **first** failure in a level, without starting the next
one. Continuing after an error would produce a partial result that looks
complete.
:::

## Schedule

```yaml
schedule: "0 5 * * *"   # five-field cron
concurrency: 1
```

Without `schedule`, the workflow is manual: it runs only by trigger in the
interface, by `brevis run`, or by `backfill`.

`concurrency: 1` limits simultaneous runs of the same workflow — it is what
keeps a `*/15` from overlapping itself when one run goes past fifteen minutes.

## Image and resources

```yaml
image: us-central1-docker.pkg.dev/example/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

Declared at the top, they are the **default for every step**. Each step can
override its own:

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # inherits the top-level image

  - id: notify
    image: ghcr.io/example/notify:0.3        # another runtime
    shell: false                             # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

This is what makes a Go fetcher cost 12 MB and 32Mi next to a 1.9 GB
`dbt build`, instead of both paying the size of the larger one. See
[Pod per step](/docs/pod-per-step/).

## Step fields

| field | | |
|---|---|---|
| `id` | **required** | unique in the workflow; the name shown in the graph and logs |
| `run` | | the command |
| `image` | | the step's image; without it, inherits the top-level one |
| `shell` | `true` | `false` runs without a shell — required on distroless |
| `depends_on` | | list of `id`s that must finish first; an entry may be `{step, label}` |
| `marker` | `false` | a step with no command, for a `start` or an `end` |
| `resources` | | `cpu`, `memory` and `limits` for that step |
| `when` | `all_success` | under what state of its dependencies this step runs — see below |
| `on_error` | | announces this step's failures — see below |

## Saying what an arrow means

A dependency can carry a label, shown on the arrow in the graph.

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
```

The bare form — `depends_on: [extract]` — keeps working and stays the normal
one. Most dependencies have nothing to say, and a label on every arrow is noise.

The labels earn their place on a **branch**: two arrows leaving the same step
with nothing written on them is a diagram that requires opening the source to
read, which is the one thing a graph exists to avoid.

## A step that does nothing

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true` is a step with no command. It runs nothing, succeeds instantly,
and appears on the graph.

It is not decoration. An `end` that depends on every branch turns *did the whole
thing finish?* into one node instead of six arrows to follow — and it behaves
like any other step, so an `end` under a failed branch is **skipped**, not
green.

**A step with no `run:` and no `marker: true` is still refused.** The two cases
must not collapse into one: an empty command is almost always a mistake, and
`marker: true` is how somebody says they meant it. A marker that also declares
`run:` is refused too — that is a file saying two things.

## Running a step only when something failed

By default a step runs when everything before it worked. `when:` changes that.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # runs precisely when extract did not make it

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # runs either way
```

| rule | |
|---|---|
| `all_success` | the default. Everything this step depends on succeeded — its **own** dependencies, as in Airflow |
| `any_failed` | at least one step this one depends on failed |
| `all_done` | everything this step depends on has finished, however it ended |

An unknown rule is refused at publish, naming what is valid. It has to be: a
`when: on_failure` read as "the default" would run on **success** — the opposite
of what it says, found on the night it mattered.

### A step that does not run is `skipped`

It is a state of its own, drawn in its own colour, and it carries the reason:
*`extract` was failed*. Before this, a step below a failure had no record at all
and the screen showed it pending forever.

`skipped` is not a failure and not a success. A step whose upstream broke did not
fail — it was never given the chance — and calling it success is a lie that
reaches the run's own status.

A trigger rule decides which steps **run**. It does not decide the run's
outcome: a `notify_failure` that delivered its message does not mean the pipeline
worked, and the run is still failed.

### `all_success` is local, as in Airflow

The rule asks about a step's **own** dependencies and nothing else. A failure in
an unrelated branch does not stop this one:

```
extract_orders ──✕                 (failed)
extract_users  ──✓── transform_users ──✓     keeps going
```

A branch **below** the failure still stops — `all_success` being local does not
mean it is absent.

This engine used to abort the whole graph at the first failure. That had a
reason: carrying on after an error produced a partial result that looked
complete, and a pipeline ran 28 days late without anyone noticing.

**What replaces that protection**, and it is not nothing:

- the run still **fails**, with the same error;
- the graph shows the failed step in red and every skipped one in its own
  colour, each saying which step stopped it;
- the alert still goes out when the run gives up.

A partial result no longer looks complete, because the run says it is not.

**What is given up**, stated plainly: an unrelated branch now writes its data on
a run that failed elsewhere. And because a run-level retry re-runs the whole
graph, a workflow with an expensive independent branch beside a flaky one pays
for that branch on every attempt.

## Announcing a step's failures

Every failure is already announced: with a webhook configured, a run that gives
up sends one message, with no block repeated in any file. `on_error` is for the
step that needs its own.

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| field | | |
|---|---|---|
| `type` | **required** | `SLACK`. An unknown value is refused at publish, naming what is valid |
| `when` | `give_up` | `attempt` announces every failed attempt, not only the last |

Two messages arrive rather than one, and that is the point: the run-level alert
says *`id_verification` failed*, which is what whoever owns the pipeline needs;
the step-level one says *`fetch_observations` failed*, which is what whoever
owns that integration needs.

**There is no `webhook` or `url` field, and there will not be.** The destination
is a credential — whoever holds it posts in the channel as if they were the
platform — and a workflow file is written by somebody who is not necessarily
allowed to choose where the company's alerts go. The file says **whether** and
**how**; the installation says **where**, through `BREVIS_SLACK_WEBHOOK`.

**`when: give_up` is the default because of whose night it is.** A step that
fails twice and passes on the third try would send two messages under the other
default, and the second would arrive after the problem was gone.

**A step that did not fail is never announced.** In a workflow where one branch
breaks, whoever owns the other branch is not woken up.

Delivery is `brevis alert`'s job — the alert is recorded in the same transaction
as the failure, so a Slack outage delays it instead of losing it.

## Tags

```yaml
tags: [analytics, dbt, daily]
```

They filter in the interface. They do not affect execution.

## Validation

Validation needs no database, so it runs in CI alongside the tests:

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

It takes a file or a directory. In a directory it matches `*.y*ml` and sorts —
two runs produce the same log, and the difference between two deploys does not
become noise.

## Publishing

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` removes from the project the workflows absent from the published list,
preserving their history. It is **not** the default, for a practical reason:
`publish one-file.yaml` must not delete the other forty-eight just because they
were not named on the command line.

## Next steps

- [Parameters](/docs/parameters/) — what changes between two runs
- [Scheduler and queue](/docs/scheduler-and-queue/) — how a workflow becomes execution
