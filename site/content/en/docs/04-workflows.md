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
| `depends_on` | | list of `id`s that must finish first |
| `resources` | | `cpu`, `memory` and `limits` for that step |
| `on_error` | | announces this step's failures — see below |

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
