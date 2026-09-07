# Flow shapes: conditionals, trigger rules and sub-flows

**Written on** 2026-09-08 · **Base** engine `v0.7.0`
**Status** proposed — not started · **TASK.md #3**
**Revised 2026-09-08** against a picture of the target (§0). Three things this
plan had missed and one it had deferred are now in it.

## 0. The picture, read node by node

The target was shown as an Airflow 3 graph, and reading it against this plan's
first draft is the fastest way to see what was missing. Every element in it:

| in the picture | Brevis today | this plan, first draft |
|---|---|---|
| `start` / `end`, **EmptyOperator** | **refused** — `Validate` rejects a step with neither `run` nor `action` | missed |
| `sales_data_extract` → `transform` → `load`, dependent and parallel | yes | — |
| `determine_load_type`, **`@task.branch`** | no | §2, §3 |
| `internal_api_load_full` drawn **skipped** (pink) | no such state | §2, and it is the real work |
| edges labelled **"changed existing data" / "additional data"** | `Edge` is `{From, To}` — nowhere to put it | **missed** |
| `sales_data_reporting`, `mlops`, `cre_integration` — **TaskGroups**, collapsible | no | §4(a) |
| `sales_data_extract [4]`, `prepare_report [6]` — **dynamic mapping** | no | **deferred to "later"** |
| `model_trained` — a **Dataset** node | no | **missed**, and it is not a graph feature at all |
| the `Layout: Left → Right` selector | the graph is left-to-right by level | — |

So the honest answer to "did you have this in mind": **branching, skipped and
grouping yes; edge labels, marker steps and datasets no; and dynamic mapping was
in the section titled "what this plan does not do".**

That last one does not survive contact with the picture. Four of its fourteen
nodes carry a `[4]` or a `[6]`. A plan for "the flow shapes we want" that
excludes the shape appearing four times is a plan for something else, so §6 now
takes it seriously — and the analysis turned out better than the deferral
assumed.

---

Today a workflow is a DAG: `depends_on` builds edges, `graph.Levels` groups them,
and a level runs in parallel. `type: chain` is sugar that turns file order into
edges.

Every step runs if its dependencies **succeeded**, and there is no way to say
anything else. The request is: conditionals on success and failure, sub-flows,
and the shapes Airflow and n8n cover.

This plan takes them in the order of value per unit of risk, and the first one
covers most of the ask.

---

## 1. What Airflow and n8n actually do, and which part is worth copying

### Airflow: trigger rules

Every task has a **trigger rule** describing what its upstream must look like:

```
all_success   (the default — and what Brevis has today, hardcoded)
all_failed    one_success      one_failed
all_done      none_failed      none_skipped      always
```

This is the whole "if error / if success" request, expressed as a property of the
step rather than as a new node type. It is a **string on a node**, and it changes
one function: the one deciding whether a step is eligible.

That is a remarkable amount of expressiveness for one field, and it is the
single highest-value item in this document.

### Airflow: branching, and the mistake it made first

`BranchPythonOperator` returns the id of the branch to take; the others are
skipped. It works, and its lesson is `SubDagOperator`: SubDAGs had **their own
scheduler and their own concurrency pool**, and they deadlocked against the
parent's. Airflow deprecated them for TaskGroups, which are pure namespacing —
no separate scheduler, no separate pool.

**That is the most useful thing to take from Airflow**, and §4 takes it.

### n8n: the IF node and the error workflow

n8n branches on **data**, with an expression over what the previous node
produced, and has a separate "error workflow" triggered when a workflow fails.

The data-branching half maps onto something Brevis already has: the context
between steps. A step publishes `{"has_rows": true}` and the next reads it. What
n8n adds is the *expression*, and expressions are the expensive part — §3.

---

## 2. Trigger rules — do this first

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

A closed vocabulary, validated at publish, naming what is valid — the shape
`runtime:` and `tools:` already use.

### The change that ripples, and it is not the rule

**`run.Status` has no `skipped`.** Today a step is pending, running, success,
failed, retrying or canceled. A step whose trigger rule is not satisfied is none
of those: it did not fail, and saying it succeeded is a lie that reaches the run's
own status.

So the first commit of this whole plan is the state machine, and it touches:

| | |
|---|---|
| `internal/domain/run/status.go` | a new state and its legal transitions |
| the run's rollup | a run whose only unfinished step was skipped is **success**, not pending |
| `StepHasSucceeded` | a skipped step has not succeeded, and a resumed run must not treat it as done |
| the graph payload and `dag.js` | a colour, and it must not be a state colour that means "went wrong" |
| the context | a skipped step published nothing, and the steps below must see its absence, not an error |

That list is why trigger rules come first and alone: the rule is a field, and
`skipped` is a concept the whole engine has to learn.

### What it buys

Every `if error` / `if success` case in the request, plus the one nobody asks for
until they need it: `all_done` cleanup, which today is impossible.

---

## 3. Conditionals on data — and the smaller version that is worth more

The tempting design is an expression:

```yaml
when: "{{ context.extract.rows > 0 }}"
```

**A mini expression language is a large commitment.** It needs a parser, a type
system to say what `>` means across a JSON `any`, a security story because the
expression comes out of a YAML somebody else wrote, and error messages that
point into a string. Every orchestrator that has one has a bug tracker full of
it.

### The cheaper shape, which composes with what exists

The step decides, and publishes a boolean:

```python
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    depends_on: [extract]
    when: all_success
    unless_empty: extract.has_rows       # a KEY, not an expression
```

The engine reads one key and tests it for truthiness. No parser, no types, no
injection surface — and the decision lives in the language the author already
writes, where it can be tested with their own test framework.

**The trade is honest and worth stating:** an expression is more expressive, and
this covers the case people actually have — "skip the rest if there is nothing
to do". It is also a decision that can be revisited *without* a migration,
because a key reference is a strict subset of what any expression syntax would
accept.

---

## 4. Sub-flows — the part with the deadlock

Two readings, and they are very different features.

### (a) Grouping — Airflow's TaskGroup

Visual and namespacing only. A group of steps that collapses on the graph and
prefixes its ids. **No new scheduler, no new pool, no new run.**

The engine's graph island already has this machinery: an SDK step's phases render
as a collapsible group with children, and `nodeHeight` already measures a node by
what it draws. Grouping steps is mostly a payload and a UI change.

**Low risk, and it is what most people mean when a DAG gets big.**

### (b) A step that runs another workflow

A real nested run, with its own id, its own history, and its own steps.

This is the one that deadlocked Airflow, and the mechanism is worth spelling out
because Brevis has the same ingredient: **`Runner.Slots` is a per-process
semaphore.** A parent step holding a slot while waiting for a child run that
needs slots from the same pool is a deadlock, and it appears only under load —
which is to say, in production.

Three ways out, and the plan should pick one before any code:

| | |
|---|---|
| **the parent releases its slot while waiting** | correct, and it changes what a slot means: it stops being "a step in flight" |
| **child runs draw from a separate pool** | simple, and it moves the ceiling somewhere nobody is looking |
| **inline the child's steps into the parent's graph at publish** | no nesting at run time, no deadlock, and the child stops being independently runnable |

The third is the one Airflow arrived at by another road, and it is the
recommendation: **expand at publish**, keep one run, one graph, one pool. What it
gives up is a child run with its own history — and that is a feature nobody has
asked for.

---

## 5. The three the first draft missed

### 5.1 Edge labels — small, and branching is unreadable without them

The picture labels two edges out of the branch: **"changed existing data"** and
**"additional data"**. Without them, a reader sees two arrows and has to open the
code to learn which is which.

`Edge` is `{From, To}` and `flowEdge` carries no label either, so this is a field
in three places and a render in one — React Flow draws edge labels natively.

```yaml
  - id: internal_api_load_incremental
    depends_on:
      - id: determine_load_type
        label: additional data
```

The awkward part is that `depends_on` is a list of strings today, so accepting an
object means accepting both forms. That is fine and it is the usual price: the
short form stays for the 95% of edges nobody labels.

**Where the value is:** not decoration. An unlabelled branch is a diagram that
requires the source to read, and the graph exists so it does not.

### 5.2 Marker steps — `start` and `end`

`Validate` refuses a step declaring neither `run` nor `action`, and the picture
opens and closes with exactly that: two `EmptyOperator`s doing nothing.

They are not decoration either. They are **join points**: `end` depends on
everything, so "did the whole thing finish" is one node instead of a reader
tracing six arrows. Airflow's own examples use them for the same reason.

Cheap to add and worth being explicit about, because "a step that runs nothing"
must be *declared* rather than achieved by leaving a field out — a step with an
empty `run:` by accident should still be refused:

```yaml
  - id: end
    marker: true            # runs nothing, on purpose
    depends_on: [publish_report, tear_down_cluster]
```

The refusal message gains a line pointing at it, which is how somebody
discovers it.

### 5.3 Datasets — and this one is not a graph feature

`model_trained` in the picture is a **Dataset**, and it is drawn as a node but it
is not a step. In Airflow a task *produces* a dataset, and another DAG declares
it *consumes* one and is scheduled when it updates.

That is **a third trigger type**, beside cron and manual. Brevis has
`schedules`, `trigger_type` on a run, and a scheduler that materialises slots
from a cron. Data-aware scheduling adds: a table of datasets and their last
update, a producer declaration on a step, a consumer declaration on a workflow,
and a scheduler path that fires on an update instead of a clock.

**It gets its own plan**, and this one only names it, for a reason worth
stating: it changes *when workflows run*, which is the scheduler's core, and
bolting it onto a document about graph shapes is how a scheduling feature gets
designed as a drawing.

---

## 6. Dynamic mapping — promoted out of "not doing"

`sales_data_extract [4]`, `internal_api_extract [4]`, `prepare_report [6]`,
`publish_report [6]`. Four of fourteen nodes. The first draft deferred this, and
that was wrong.

### Where the list comes from, and why it is already solved

The natural source is the context feature that shipped last week:

```python
# extract
context.set(partitions=["2026-09-05", "2026-09-06", "2026-09-07"])
```

```yaml
  - id: load
    depends_on: [extract]
    for_each: extract.partitions
    run: python load.py            # BREVIS_MAP_VALUE, BREVIS_MAP_INDEX
```

A key reference, not an expression — the same decision §3 makes, for the same
reason.

### What it actually costs, which is less than the deferral assumed

Three things had made this look expensive, and two of them dissolve:

| feared | actually |
|---|---|
| "the graph changes during a run" | **the DAG's shape does not.** `for_each` changes how many *instances* a node has, not which nodes or edges exist. `graph.Levels` operates on the definition and is untouched |
| "the UI has to redraw a growing graph" | the graph is **computed at request time** from the definition plus `task_runs`. A mapped node renders `[4]` by counting rows — the same read that already produces the status |
| "`task_runs` cannot hold it" | true, and it is the real cost: the row is keyed by `(run_id, node_id, attempt)` and needs a **`map_index`**. Airflow reached the same column, with `-1` for unmapped |

So the work is: a `map_index` column, a `for_each` field validated at publish, the
runner expanding a node into N tasks against the same slot semaphore, and the
graph counting instances. It is a real feature and it is not the redesign the
first draft implied.

### The bound nobody has to invent

**The context ceiling caps the fan-out.** A list has to fit in 4096 bytes, so a
map is tens or hundreds of items and never a hundred thousand.

That is a limit worth keeping rather than working around. Unbounded fan-out is
how an orchestrator's scheduler becomes the bottleneck, and every engine that
allows it has grown a second limit to take it back. Here the platform's ceiling
does it for free, and the error when somebody exceeds it already names the
largest keys.

### Where it goes in the order

**After `skipped` and trigger rules, before sub-flows.** It shares the state
machine work with the first, it has no interaction with the third, and it is the
shape the picture uses most.

---

## 7. Order of work

| | | risk |
|---|---|---|
| 1 | `skipped` in the state machine, everywhere it ripples | **the highest in this plan**, and it carries no feature on its own |
| 2 | `when:` trigger rules, validated at publish | low, once 1 is done |
| 3 | edge labels, and `marker: true` steps | low — a field in three places, and a refusal that gains a line |
| 4 | `unless_empty:` — a key, not an expression | low, and it composes with the context feature |
| 5 | `for_each:` — `map_index`, the expansion, the `[4]` on the card | **medium**, and the second-largest thing here |
| 6 | grouping on the graph (TaskGroup-shaped) | low, mostly UI |
| 7 | sub-flows by expansion at publish | medium, and only after everything above has settled |

Datasets (§5.3) are not in this list. They are a scheduling feature and they get
their own plan.

Step 1 first, alone, and with no user-visible change. That ordering is
deliberate: a state machine change reviewed alongside a feature is a state
machine change nobody reviews, and this one reaches the run's status, the resumed
run, the screen and the context.

## 8. How it is proven

- **A skipped step does not make its run fail**, and does not make it succeed
  either while something is still running.
- **A resumed run does not treat a skipped step as done** — `StepHasSucceeded`
  says no, and the step runs when the rule is satisfied on the retry.
- **A step below a skipped one sees its absence**, not an error and not a stale
  value from a previous attempt.
- **`when: any_failed` fires exactly when something failed**, and — the assertion
  that actually bites — **does not fire when everything succeeded**. A rule that
  always fires passes the first half.
- **The default is unchanged**: a workflow with no `when:` behaves exactly as
  today, asserted by the existing corpus of examples.
- **A cycle through a conditional edge is still refused at publish.**
- **A mapped step produces N task_runs and one node on the graph**, and the count
  on the card matches the rows — a `[4]` that says four while three ran is the
  badge-that-lies failure in a new place.
- **A mapped step whose list is empty produces zero instances and is `skipped`**,
  not `success` with nothing done.
- **A `marker: true` step still refuses an accidental empty `run:`** — the two
  cases must not collapse into one.

## 9. What this plan does not do

- **Datasets and data-aware scheduling.** §5.3 — its own plan, because it changes
  when workflows run rather than how one is drawn.
- **An expression language.** §3.
- **Unbounded fan-out.** §6 — and the bound is inherited rather than invented.
- **Retries as a flow shape.** They already exist per step and belong there.
