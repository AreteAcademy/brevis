# Flow shapes: conditionals, trigger rules and sub-flows

**Written on** 2026-09-08 · **Base** engine `v0.7.0`
**Status** proposed — not started · **TASK.md #3**

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

## 5. Order of work

| | | risk |
|---|---|---|
| 1 | `skipped` in the state machine, everywhere it ripples | **the highest in this plan**, and it carries no feature on its own |
| 2 | `when:` trigger rules, validated at publish | low, once 1 is done |
| 3 | grouping on the graph (TaskGroup-shaped) | low, mostly UI |
| 4 | `unless_empty:` — a key, not an expression | low, and it composes with the context feature |
| 5 | sub-flows by expansion at publish | medium, and only after 1–4 have settled |

Step 1 first, alone, and with no user-visible change. That ordering is
deliberate: a state machine change reviewed alongside a feature is a state
machine change nobody reviews, and this one reaches the run's status, the resumed
run, the screen and the context.

## 6. How it is proven

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

## 7. What this plan does not do

- **Dynamic task mapping** (Airflow's `.expand()`) — a step fanning out over a
  list produced at run time. It is a genuinely useful shape and it changes the
  graph *during* a run, which the UI, the levels and the state machine all assume
  is fixed. Its own plan, later.
- **An expression language.** §3.
- **Retries as a flow shape.** They already exist per step and belong there.
