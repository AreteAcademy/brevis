# Native dlt integration, and the third executor it needs

**Written on** 2026-09-09 · **Base** engine `v0.11.0`, `sdk/v0.56.0`, `brevis` (PyPI) `0.2.1`
**Status** accepted — `TASK.md` #7, #8 and #10
**Reviewed on** 2026-09-09 against the tree; three corrections are in §8 at the
end, and one of them changes what ask two costs.

Three asks of increasing size. The first is a word in a list, the third is an
execution target. They are independent and can ship in that order; only the third
is architecture.

The thesis behind them: **Brevis does not exclude, it integrates.** The tools
teams already use for extract-and-load are not competitors to an orchestrator —
most of them are missing one.

---

## 1. Why dlt, and why not Airbyte

Both were evaluated against a real pipeline: 48 workflows, 205 dbt models, 17
upstream APIs. The two answers are opposite, and the reason is the scheduler.

**Airbyte schedules.** Three modes per connection — Manual, Scheduled (an
interval from a list) and Cron (a Quartz expression with a timezone). So adopting
it means a second scheduler in the cluster: two places a cadence is defined, two
state stores, two sets of failure modes. And its documented guarantee is *"syncs
will initiate with a schedule accuracy of +/- 30 minutes"*, which is not a
cadence for the six of our flows that run every fifteen or thirty.

Its catalogue also does not reach us: **0 of our 17 upstreams** have a connector
among the 600 in the OSS registry. They are Brazilian and Portuguese government
and scientific APIs — CEMADEN, CPTEC/INPE, INMET, ANA, SNIRH, IPMA, NASA FIRMS,
Copernicus, Overpass, IBGE.

**dlt does not schedule.** Its own deployment page says *"dlt runs anywhere Python
runs"* and points at GitHub Actions, Snowflake and their managed platform. It is
a library and expects an orchestrator around it.

| | scheduler | shape | relation to Brevis |
|---|---|---|---|
| Airbyte | yes, ±30 min | platform | competes |
| dlt | **no** | library | **complements** |

A dlt user today has extract and load solved and scheduling unsolved, and reaches
for Airflow, Dagster or Prefect — all heavier than the gap. That gap is Brevis's
shape exactly: one pod per step, a cron, retries with a real backoff since
0.10.0, run params, and alerting with no per-workflow block.

---

## 2. Ask one — dlt is missing from the vocabulary

`internal/domain/runtimes/runtimes.go:84`:

```go
Tools = []string{DBT, Spark, Airbyte, Soda, SQLMesh, Meltano, DuckDB, Pandas, Polars, Airflow, Terraform}
```

Airbyte and Meltano are there. dlt is not, and it is the one of the three that
fits an orchestrator without competing with it.

**One caveat worth designing around rather than ignoring.** The detector maps a
command's first word, and a dlt pipeline is almost always `python
my_pipeline.py` — which correctly detects Python and says nothing about dlt. A
tool chip that never lights up is worse than no chip. Either the detector learns
a second signal (`dlt` on the line, an import in a declared entrypoint), or the
step declares `tools: [dlt]` and the chip is honest about being a declaration.
Pick one and say which.

Cost: a constant and a test. Value: a user scanning the UI sees that Brevis knows
what they are running.

### Built on 2026-09-09 — and the caveat was answered, not ignored

**Both**, split along what each can honestly claim.

The detector learned `dlt` in two places it can actually read: as a command head
(`dlt pipeline orders run`, the CLI) and as a marker anywhere in the fragment
(`python -m dlt …`, where the head is `python`). Those are the same two hooks
dbt has, for the same reason.

It deliberately did **not** learn to guess from a filename. `python
pipelines/orders.py` reads as Python and nothing else, and that is the case the
proposal was right to raise — it is how most dlt steps are written. The answer
for it is `tools: [dlt]`, which the chip already draws with a solid border
rather than a dashed one. So the chip does light up for everybody; what changes
is whether it is claiming *"you told me"* or *"I read it"*, which is a
distinction the panel already spells out and the SDK badge beside it depends on.

The four rows are in `docs/RUNTIME.md`, and each one is a case in
`TestDLTIsDetectedWhereItCanBeAndNotWhereItCannot` — including the two that
expect the detector to stay quiet, which are the ones with teeth.

One thing found on the way: `TestTheIdsAreStableKeys` promised that a new id
"needs a CSS token and a line in the YAML documentation", and checked that
against a hard-coded string of ids. It could only ever say *"that id is new"* —
adding the id to the string was enough to ship one with no label and no
documentation. It reads `dag.js`, `app.src.css` and `RUNTIME.md` now, in both
directions, and fails if any of the three moves out from under it.

---

## 3. Ask two — bridge the run window to dlt's incremental cursor

This is the piece **only Brevis can offer**, and the one that turns an
integration into a reason to adopt.

dlt tracks incrementality in state it persists in the destination: a cursor
advances, and the next run reads from where the last one stopped. Brevis knows
something different and better for the same question — per run, as a snapshot
stored on the run itself:

| param | what it says |
|---|---|
| `adjusted_at` | the clock this run should read |
| `interval_start` / `interval_end` | the slot this run covers, end excluded |
| `previous_success_at` | the slot of the last run that passed |

Those are two authorities over one question, and dlt's is the weaker one: it only
moves forward.

The `brevis` PyPI package already exists for exactly this kind of bridge — 14 KB,
zero dependencies, Python ≥3.9, already shipping `run.window()`, `run.now()` and
`run.auto()`. A dlt helper there would mean:

- a **backfill of an old slot** reads the window that slot had, instead of
  whatever the stored cursor says today;
- a **lost run** is recovered from `previous_success_at`, not from state that
  only advances;
- **two runs of the same slot** read the same window, which is what makes a
  retry idempotent.

dlt alone cannot do the first without dropping state, and dropping state to
re-run one day is the blunt instrument its refresh options offer (`drop_data`,
`drop_resources`, `drop_sources`).

**Before writing this, read the cursor-based section of dlt's incremental-loading
docs.** The concepts are published — cursor-based loading, a lag/attribution
window, persisted state — but the overview page does not carry the parameter
names, and a spec written from memory of an API loses its reader on the first
signature.

---

## 4. Ask three — a third executor, for a host Brevis does not own

The requirement, from the consumer: **dlt may run on a machine Brevis did not
create.** An EC2 instance, a VM, a box in another cluster. Brevis should dispatch
to it by host, stream its output, and be able to cancel it — and should still be
able to run the same step as a pod when that is how it is configured.

### The code already expects a third one

`internal/execution/executor.go` is a three-method interface — `Name`, `Execute`
returning a channel of events, `Cancel`. `TaskExec` is, in its own words,
"deliberately poor: the executor knows nothing of workflows, dependencies or
schedules", and "the runner is what chooses the executor, not the executor
itself". The `EventContext` comment closes it:

> *it travels as an event for the same reason the phases do: the runner collects
> it without knowing whether it came from a file on this disk or from a pod's
> termination message, **so a third executor costs the runner nothing**.*

There are two today, `kubernetes` and `local`. This is the third.

### Shape

A step names a host instead of an image:

```yaml
- id: extract
  host: dlt-runner-01          # instead of `image:`
  run: python pipelines/orders.py
```

`internal/execution/remote` implements `Executor`, talks to a small agent on the
host, and maps the agent's stream onto `Event`. The runner does not change.

### The five things that decide whether it is good

Named because each is a place where a thin version would ship and then hurt.

**1. Streaming and the broken connection.** `Execute` returns `<-chan Event`; over
HTTP that is SSE or chunked NDJSON. The question is not the encoding, it is what
a dropped connection means. If the engine reconnects, the agent must hold the
run's output long enough to replay from an offset. If it does not, a network blip
turns a healthy 40-minute load into a failed step. State which, and make the
agent's buffer bounded.

**2. Cancel across a process boundary.** `Cancel(ctx, execID)` must reach a
process on another machine. The agent needs an `execID → pid` map that survives
its own restart, or cancel is best-effort — which is a legitimate choice if the
UI says so instead of showing a spinner that never resolves.

**3. Secrets, and this is the real decision.** `TaskExec.Secrets` is
variable-name → `secret-name/key`, deliberately unresolved, and the field's own
comment says why: *"if the value arrived resolved here, it would pass through the
dispatcher, through the task assembly log and through any TaskExec dump somebody
writes later."*

A host outside the cluster cannot read a Kubernetes Secret. So either

- **the agent resolves**, with its own credentials against its own store — the
  engine keeps its property, and the operator now has two secret stores to keep
  in agreement; or
- **the engine resolves and sends over mTLS** — one store, and the value now
  passes through exactly the path that comment was written to avoid.

Both are defensible. Choosing silently is not. Whichever it is, write the
trade-off next to the code, because the next reader will ask.

**4. Liveness.** An agent that dies mid-run is indistinguishable from one running
a long step. A lease the agent renews, or a heartbeat with a stated timeout, is
the difference between a step that fails in ninety seconds and a run that hangs
until somebody notices.

**5. Who may be an agent.** A shared token is the obvious start and its rotation
the obvious gap. Say what the first version does and what it does not.

### Keep it generic

The remote executor should know nothing about dlt. dlt is its **first consumer**,
not its shape — the same target serves a Spark submit box, a licensed tool that
cannot be containerised, and a GPU host. An executor with `dlt` in its vocabulary
will be rewritten for the second use, and the second use is close.

---

## 5. What not to build

**Not a dlt runtime image as "the integration".** Running dlt under Brevis
already works today: it is a Python step with a pip install. Shipping an image
and calling it integration would be convenience presented as architecture, and it
answers none of §3 or §4.

**Not an Airbyte-style control plane.** The value of these tools is the connector
and the library. The platform is the part Brevis replaces.

**Not a dlt-shaped executor.** See above.

---

## 6. Acceptance

1. A workflow with `host:` runs on a machine outside the cluster, streams logs to
   the UI in real time, publishes context to the next step, and cancels from the
   UI.
2. The same workflow with `image:` instead of `host:` runs as a pod, byte-identical
   result, no YAML change beyond that line.
3. `brevis backfill <slot>` on a dlt pipeline reads the window that slot had, not
   the stored cursor's.
4. Killing the agent mid-run surfaces as a failed step within a stated timeout,
   not a hung run.
5. The secret decision of §4.3 is documented with its trade-off, in the code.

---

## 7. A note on the consumer's own position

`zarv-data-pipeline` rejected dlt for itself on 2026-09-07, and the objections
were specific to it: +371 MB on a 145 MB image, a schema normaliser it would
immediately disable because bronze stores an opaque `TO_JSON(data)` payload and
dbt owns every bit of typing in silver, and a third implementation of an
`ingestion_id` proven byte-for-byte twice.

**Two of those three have since relaxed.** The consumer has decided new ETL need
not follow the `ingestion_id` contract, which removes the identity objection for
anything built from here. And none of the three was ever an argument against
Brevis integrating dlt: a user who wants schema inference and does not own their
modelling is dlt's audience, and is not this consumer.


---

## 8. Review against the tree

Every claim above was checked against the code rather than taken. They hold,
with three corrections — and the third is the one that matters.

### The detector already has a second signal, and the YAML already has the third

§2 says the detector "maps a command's first word" and asks for a choice between
teaching it a second signal and declaring the tool. Both already exist:

- `markers` in `runtimes.go` names a tool **anywhere in a fragment**, not only at
  the head. That is how `python -m dbt.cli` is detected today, and `dlt` there
  catches `python -m dlt` and the `dlt` CLI.
- `tools:` is already a field of a step (`spec.go:133`), already validated
  against `runtimes.Tools` (`workflow.go:812`), and a declared chip is already
  drawn differently from an inferred one -- `SourceDeclared` against the dashed
  border of an inference.

So the answer is not "pick one": adding `dlt` to `Tools` makes the declaration
work the same hour, and a `markers` entry catches the commands that name it. The
common `python my_pipeline.py` still says only Python, and that is correct --
the chip stays honest by being a declaration when it is one.

**Ask one is a constant, a marker and a test.** It was already nearly free and it
is now slightly freer.

### The Python package is 30.8 KB of source, not 14

It grew: `run.py` (the auto params) and `metrics.py` landed after this was
written. Still zero dependencies, still Python ≥3.9, and the gate that says so
(`python-check.sh`) still passes -- which is the property ask two depends on, not
the number.

### Ask three IS `TASK.md` #10, and that is the correction that matters

`TASK.md` #10 -- "Beyond Kubernetes: Cloud Run, Lambda, ECS, EC2" -- and §4 here
are the same executor. Listing them apart is how a thing gets built twice, or
built once by somebody who did not know the other entry existed.

They were written independently and they agree, which is the strongest evidence
either is right. [`2026-09-08-backlog.md`](2026-09-08-backlog.md) §8 says the
first deliverable is *a decision about the return path*, and names
`terminationMessagePath`, `follow=true` logs and cancellation as the three things
a non-Kubernetes target has no equivalent for. §4 above names five: streaming and
reconnection, cancel across a process boundary, secrets, liveness, and who may be
an agent.

The union is the design agenda, and the overlap is not a coincidence -- both
lists are derived from the same interface.

One thing this plan adds that the backlog did not: **§4's insistence that the
executor know nothing about dlt.** The backlog treated the targets as a list of
platforms; this frames the first one as a HOST the engine does not own, which is
the more general shape and the one that also serves a Spark submit box or a
licensed tool that cannot be containerised.

### Unverified, deliberately

§3's account of dlt's incremental cursor is taken on trust: this review checked
the Brevis side of the bridge, not dlt's. The plan already says to read the
cursor-based section of its docs before writing the helper, and that instruction
stands -- a helper written from memory of an API loses its reader on the first
signature.
