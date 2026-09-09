# Workflows

> The complete YAML format — steps, dependencies, images, resources and schedule.

*https://brevis.sh/en/docs/workflows/ · brevis.sh docs (en)*

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
[Pod per step](/en/docs/pod-per-step/index.md).

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
| `runtime`, `tools` | | the step's language and tools — see [Step runtime](/en/docs/runtime/index.md) |
| `when` | `all_success` | under what state of its dependencies this step runs — see below |
| `unless_empty` | | a context key that decides whether there is anything to do — see below |
| `for_each` | | a context key holding a list; the step runs once per element — see below |
| `group` | | draws this step inside a named, collapsible box — see below |
| `uses` | | another workflow whose steps take this one's place — see below |
| `on_error` | | announces this step's failures — see below |

## Running a step only when there is something to do

```python
# in extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

`unless_empty` names a **key**, not an expression. The step decides and
publishes the answer; the engine reads one key and asks whether it is empty.

| counts as empty | counts as present |
|---|---|
| `false`, `0`, `""`, `null`, `[]`, `{}` | everything else |

`"false"` as a **string** is present. A step that published those five
characters published something, and guessing that it meant a boolean is how a
rule starts having opinions its author cannot see. Publish a real boolean.

### Why not an expression

The tempting design is `when: "{{ context.extract.rows > 0 }}"`. A mini
expression language is a large commitment: a parser, a type system to say what
`>` means across a JSON `any`, a security story because the expression comes out
of a YAML somebody else wrote, and error messages that point into a string.
Every orchestrator that has one has a bug tracker full of it.

Here the decision lives in the language its author already writes, where their
own test framework can reach it.

### A missing key is a failure, not an empty value

If the key is not there, the step **fails** and the message names it alongside
what the publisher actually did write:

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

The alternative — reading a missing key as "empty, so skip" — turns a typo into
a step that stops running silently and forever, with nothing anywhere saying
why. That is the worst outcome this feature can have.

The key is always qualified by the step that publishes it, and that step has to
be one this one depends on. Both are refused at **publish**: a step gated on a
key it can never see would be skipped forever, and finding that out when a
nightly stops running is too late.

## Running a step once per element

```python
# in extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

The step runs once per element, each with a row, a retry and an exit code of its
own. The graph shows **one node with `[3]` on it**, not three nodes.

| variable | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`, `1`, `2` … |
| `BREVIS_MAP_VALUE` | the element |

A JSON string arrives **without its quotes**, so `for_each` over `["2026-01"]`
hands a shell `2026-01` and not `"2026-01"`. Anything else — a number, an
object, a list — arrives as its JSON.

Neither variable exists on an unmapped step. One that is always there and always
empty teaches whoever reads the environment to ignore it.

### The shape of the DAG does not change

A mapped step is still one node with one set of edges. Only the number of rows
under it varies, so the layout is what it always was and the `[3]` is counted
from those rows at read time — nothing is stored, so nothing has to be kept in
sync and a wrong count cannot outlive a fixed query.

While instances are still going the card reads `[2/3]`.

### One failed instance fails the step

Three partitions loading and one not is a step that did not do its job, and the
steps below it see that. A node that goes green because most of it worked is a
badge that lies.

**A retry redoes only the instances that failed.** Eighteen partitions that
worked are not reloaded because two broke.

### An empty list is `skipped`, not success

A step that did nothing because there was nothing to do did not succeed at doing
it. A green node over zero instances is exactly the kind of thing somebody
builds a dashboard on.

A value that is **not a list** fails, and so does a key that is not there — the
same policy `unless_empty:` has, and for the same reason: a typo that silently
produced zero instances would disable the step forever.

### The fan-out is bounded, and the bound is inherited

The list travels in the context, and the context has a 4096-byte ceiling that
comes from the kubelet. A workflow cannot ask for ten thousand pods without
first finding a way to say so in four kilobytes. That is a limit worth keeping
rather than working around.

### What a mapped step publishes

Its output is recorded on each instance's row and is **not** visible to the
steps below. Four instances publishing under one step's name is four values for
one key, and there is no answer to `context.String("load.bucket")` that is not a
guess. The step says so in its log rather than dropping it quietly.

## Grouping steps on the graph

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

The steps are drawn inside a named box that collapses — Airflow's TaskGroup,
for the case where a DAG gets big enough to stop being readable.

**Visual only.** Airflow's TaskGroups also *prefix* the ids inside them, so
`extract` becomes `sales.extract`. This does not: prefixing would change every
`depends_on`, every context key and every recorded row in an existing workflow,
for a feature whose whole value is that a big graph reads better. Namespacing
can be added later — it is a strict addition to this.

A group is a **label**, not a container. Steps keep their global ids, a group
may span levels, and nothing about execution changes.

Clicking the group's name collapses it: its steps disappear and the arrows that
crossed the boundary point at the box instead.

## Reusing another workflow

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # another workflow in the same publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

At **publish**, `ml_training`'s steps take that step's place, prefixed with its
id, and the `uses` node disappears:

```
prepare → mlops.train → mlops.evaluate → report
```

They arrive as one collapsible group, so the graph shows what the file said.

**Expanded at publish, not run at run time.** A step that triggered a child
*run* and waited is the design that deadlocked Airflow, and this engine has the
same ingredient: the pod ceiling is a per-process semaphore, so a parent holding
a slot while waiting for a child that needs slots from the same pool hangs — and
only under load, which is to say in production. One run, one graph, one pool.

What it gives up is a child run with its own id and its own history.

### The rules

| | |
|---|---|
| the child must be in the **same publish** | not merely already published |
| arrows | into the step become arrows into each of the child's **roots**; out of it, out of each of its **leaves** |
| the child's `image`, `env`, `secrets`, `resources` | materialised onto each step, so it runs in what its own file said |
| the child's `unless_empty` / `for_each` keys | move with the prefix |
| nesting | flattened, depth first |
| a circle | refused at publish, naming the chain |

**Same publish, and not "already published", is the important one.** Expansion
that read the database would make `brevis validate` — which touches no database,
on purpose — answer a different question from `brevis publish`, and the file
that passed CI would be the file that failed the deploy.

The child is still publishable on its own: expansion **copies**, it does not
consume. A step cannot both `uses:` and declare `run:`, `action:` or `marker:` —
that is a file saying two things.

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
a run that failed elsewhere.

### A retry re-runs only what failed

A run's retry does not redo the whole graph. A step that already succeeded on an
earlier attempt of the **same run** keeps its result and is not run again — the
way a cleared DAG run behaves in Airflow.

```
attempt 1   extract ✓    load ✕    report (skipped)
attempt 2   extract –    load ✓    report ✓            extract is not re-run
```

It keeps its original row: its duration, its log and what it published are the
first attempt's, because that is when the work happened. **The step below it
still reads what it published** — the run's context is stored, not rebuilt.

The distinction that matters: this is about **this run's** earlier attempts.
A step that succeeded in *yesterday's* run runs normally today. (That other
question exists too, and it is what tells the SDK whether it is a step's first
time — see `BREVIS_RUN_FIRST`.)

A step that was **skipped** is not settled: it never ran, and the retry decides
about it again with the new attempt's outcomes.

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

- [Parameters](/en/docs/parameters/index.md) — what changes between two runs
- [Scheduler and queue](/en/docs/scheduler-and-queue/index.md) — how a workflow becomes execution
- [Context between steps](/en/docs/context/index.md) — what one step tells the next
