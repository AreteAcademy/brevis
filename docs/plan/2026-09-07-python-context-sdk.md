# A Python SDK for context, and the contract that makes it possible

**Written on** 2026-09-07 · **Base** engine `v0.7.0`, `sdk/v0.53.0`
**Status** in progress — steps 1, 2, 4 and 8 done on 2026-09-07. What is left:
`sdk/context` in Go (§6), the context on the graph (§7), and publishing to PyPI.
**Decided 2026-09-07** (§1.1): the reader is always another step of the same
run, and there is one write path that fails loudly. No context API, and the
reason is written down rather than assumed.

Data engineering is a Python community. The Go SDK is an ETL library — drivers,
pagination, checkpoints, ingestion ids — and porting it would be a year of work
producing a worse copy of tools Python already has.

This is the other thing. A **small** package that does one job: let a step read
what the steps before it published, and publish something for the steps after
it. Nothing else. A team writes their pipeline in pandas, Polars, dbt or plain
`requests`, and uses Brevis for the one thing an orchestrator is for.

```python
from brevis import context

bucket = context.get("extract.bucket")
context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
```

That is the entire surface.

---

## 1. The question that decides everything: how does it reach the engine?

It does not. **There is no API call, no token, no port and no network.**

### 1.1 The API was seriously considered, and the answer to one question closed it

The first shape asked for was an API the SDK calls, with the context managed
server-side. It is what Airflow, Prefect and Dagster do, and it was not dismissed
for being unlike this repository. It was dismissed by an answer:

> **Who reads the context?** Only the steps of the same run.

That single answer removes everything the API was buying. Its advantages over
injected input are: reading from outside the run, reading context produced
*after* your step started, reading your own writes back, and no size ceiling.
With the reader always inside the run, the first is out of scope, the second is
a race in a DAG (if you depend on the value you wait for it; if you do not, you
are reading a step that may not have run yet), and the third is a local variable.

What it would have cost is concrete, and two of the three are not obvious:

- **There is no machine authentication in this engine.** Every route is behind a
  signed session cookie for a browser. A context API means minting a token per
  run, scoping it to that run and step, expiring it, and injecting it into every
  pod — a new authentication surface on the one process that is exposed to the
  network.
- **`serve` and `scheduler` are different deployments.** The process that runs
  a step is not the process that would answer its writes. Today `serve` can be
  down and pipelines keep running; with a context API, a `dbt build` that
  succeeded after forty minutes fails on the `set()` that follows. That cost was
  accepted in the abstract — it is worth seeing stated concretely.
- **`brevis run` has no server**, so local mode would need a loopback API or a
  second path, and a second path is a second failure mode.

The one honest point against the chosen design is that `BREVIS_INPUT` and
`BREVIS_OUTPUT` read as strange. **They should never be seen.** They are the
transport, the way `BREVIS_RUN_ID` is the transport behind `sdk.Run`'s knowing
it is under the engine. What a consumer writes is `context.get(...)` and
`context.set(...)`, and that is identical whether the bytes travel over HTTP or
through a file.

The exception is the step with no SDK at all — the `bash`+`jq` one — which sees
the variables directly. That is the price of the contract being language-neutral
rather than a library, and it is the price worth paying.

**If the reader ever moves outside the run**, this decision is the thing to
revisit, and the cost is the token mechanism above rather than a redesign: the
storage, the keys and the SDK surface do not change.

### 1.2 The mechanism

That is not a limitation worked around; it is the design, and
[`2026-09-05-contexto-entre-passos.md`](2026-09-05-contexto-entre-passos.md)
already ruled out the alternatives for reasons that hold harder in Python:

| route | why not |
|---|---|
| the task calls the engine's API | needs network from the pod to the engine, authentication, and **the engine to be up when the task finishes**. It couples the data to the server's lifetime, and it means shipping a credential into every step |
| a shared volume | needs the PVC configured, and on RWX it becomes global state between runs with nobody having asked for it |
| **the container's termination message** | it is Kubernetes' own mechanism, it exists for exactly this, **and the engine already reads it** |

The engine already parses `containerStatuses[].state.terminated.message` — it is
where the exit code comes from (`internal/execution/kubernetes/pod.go`). The
return path exists, costs zero new permissions, and works in any language,
because it is *writing a file*.

So the Python SDK's whole job is: **read one environment variable, write one
file.** Which is why it can be pure stdlib, zero dependencies, and testable
without a cluster, a database, or a running engine.

### What that means for the consumer

```
BREVIS_INPUT    JSON: {"<step id>": {<what it published>}}
BREVIS_OUTPUT   a path to write one JSON object to
BREVIS_RUN_ID   the run this belongs to — already injected today
```

In Kubernetes `BREVIS_OUTPUT` is `/dev/termination-log`. Locally it is a
temporary file the engine reads afterwards. **The task never learns the
difference**, and neither does the SDK.

A step with no SDK at all still participates, which is the test of the contract:

```bash
window=$(jq -r '.ingest.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

If it does not fit in a line of shell, the contract is wrong.

---

## 2. What has to exist first, and it is not the Python package

The Python SDK is **useless until the engine implements the contract**, and the
engine does not: `BREVIS_INPUT` and `BREVIS_OUTPUT` appear nowhere in the tree
today. That plan is written and unexecuted.

So the order is not negotiable:

1. **The engine's contract** — inject `BREVIS_INPUT`, set `BREVIS_OUTPUT`, read
   the termination message back, persist it. This is
   [`2026-09-05-contexto-entre-passos.md`](2026-09-05-contexto-entre-passos.md),
   which stays the authority on the engine side.
2. **Persistence** — §4 below, which is the part that plan left open and this
   request pins down.
3. **The Python package** — §5, the reason for this document.
4. **The cross-language proof** — §8. Without it the feature is "works if you
   use Go".

Shipping 3 before 1 gives a package that reads an environment variable nobody
sets. It would install, import, return `None`, and teach the community that
Brevis's Python support does not work.

---

## 3. Session, run, attempt — the ids, and which one owns the context

The request asks for "an id identifying that session, and each run with its
own". The engine already has this and it is worth naming precisely, because
choosing the wrong one is what makes a context store leak across runs.

| what | where | injected as |
|---|---|---|
| **run** | `runs.id` (UUID) | `BREVIS_RUN_ID` |
| **step** | `task_runs.node_id` | the step's own id in the YAML |
| **attempt** | `task_runs.attempt` | `BREVIS_RUN_ATTEMPT` |

**The run id is the session id.** It is already injected into every step, it
already survives a retry, and every `task_runs` row already points at it.
Inventing a second identifier alongside it would be a second thing to keep in
sync, and the first time they disagreed the context would silently belong to
nobody.

So: **context is keyed by `(run_id, node_id)`, and lives as long as the run.**

### The line this draws, and it has to be drawn out loud

Somebody will want `ingest.watermark` from **yesterday's** run. It is the most
natural request in the world and it is a different feature.

Context between steps lives inside a run and dies with it. State between runs is
persistence, with different questions: where does it live, who reads it, what
happens when two runs overlap. Blurring them turns the context into an
accidental database nobody designed.

The answer is an explicit **no**, with the right path pointed at next to it —
which today is the destination table the pipeline already writes to, or the
credential store's shape if it is genuinely small state.

---

## 4. The database, and what "recover the context if it fails" means

The request says the context must be saved in the database so it can be
recovered on failure. That is right, and the reason is sharper than it looks.

**A step that fails and re-runs replaces its own output.** The previous
attempt's output cannot survive — it described work that did not finish.

**A run that resumes is the case that forces persistence.** The engine already
answers `StepHasSucceeded`, so a resumed run skips `ingest` — and
`transform` still needs what `ingest` published. Memory is not enough, because
the process that held it is gone.

### The shape

One column, on the row that already records the task:

```sql
ALTER TABLE task_runs ADD COLUMN saida JSONB;
```

It goes on `task_runs` and not on `runs` because that is where the answer to
"did this step succeed" already lives, and the two have to move together. A
separate table would let them disagree.

`BREVIS_INPUT` is then assembled by reading the successful `task_runs` of the
run — which is a query the engine already makes.

**The column name is Portuguese to match its neighbours** (`erro`,
`iniciado_em`, `criado_em`): the applied schema is an exception the
English-only plan already records, and a migration adding a column with a
different convention from the ten beside it is worse than either convention.

### What is written, and when

The engine writes it **once, when the task finishes**, from the termination
message. Not on every `set()` — the SDK accumulates in memory and writes the
file at exit. Three consequences, all of them wanted:

- a step that crashes hard writes nothing, and a crashed step's output must not
  survive;
- there is no chatter: one write per task, not one per key;
- `set()` is free, so nobody structures their code around avoiding it.

### The size ceiling is not ours, and that is why it holds

The termination message is truncated at **4096 bytes**.

This looks like a limitation and is the best part of the design. Every
"context between tasks" feature fails the same way: somebody puts *data* where
only *context* fits. Airflow's XCom stores in the metadata database, and the
known consequence is people pushing DataFrames through it until the
orchestrator's database becomes the pipeline's bottleneck.

A ceiling that is not ours is stronger than a policy we would have to enforce.
Four kilobytes hold a watermark, a count, a list of partitions. They do not hold
the data — and that is exactly the boundary.

---

## 5. The Python package

### 5.1 The surface

The request sketched `BrevisContext.get('bucket')` and
`BrevisContext.set(ctx)`. Keeping the shape, with three corrections that the
first real pipeline would have forced anyway.

```python
from brevis import context

# read
bucket   = context.get("extract.bucket")         # always qualified by the step
missing  = context.get("extract.nope", default=0)  # absent is not an error
extract  = context.of("extract")                 # everything one step published

# write
context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
context.set({"partitions": ["2026-09-06", "2026-09-07"]})
```

**A module, not a class.** `BrevisContext.get(...)` as a classmethod suggests
there could be two of them. There cannot: a process is inside exactly one step
of one run. A module says that in the import.

**A read is always qualified by the step that wrote it.** `get("bucket")` with
no step is refused, naming the steps that published that key.

This was the weakest part of the first draft, which let a bare key search the
declared inputs and only complained on a collision. That is precedence by
accident: it works until a second step publishes `bucket`, and then somebody
spends an afternoon on a value that came from the wrong place. The one-line
convenience is not worth the class of bug.

**Isolation is by construction, not by policy.** A step writes only into its own
namespace — `set()` takes no step argument, because the only namespace a process
can write is its own — and `Validate` already refuses a workflow with two steps
sharing an id. So `extract` and `transform` cannot overwrite each other's keys,
and no rule has to be enforced for that to hold.

Writing is isolated; reading is shared and explicit. That asymmetry is the whole
model:

```python
context.set(bucket="s3://landing/2026-09-07")   # only into "extract"
context.get("extract.bucket")                   # any step, named
context.of("extract")                           # everything one step published
```

**`set()` merges, and the last write of a key wins.** Called twice with the same
key, the second wins — but the *first* call's other keys survive. Replacing
wholesale would make a helper function that publishes one key silently erase
what `main()` published.

### 5.2 What it refuses, and why each refusal exists

The package is small, so most of its code is the refusals. That is correct: the
failures of a context feature are all silent, and each guard turns one of them
into a message.

| refusal | the silent failure it replaces |
|---|---|
| a value that is not JSON-serializable | a `datetime` becomes `"..."` or vanishes, depending on who serializes it. It raises, naming the key and the type, with `.isoformat()` suggested |
| more than 4096 bytes | the termination message is **truncated, not rejected** — and a JSON cut mid-string is not JSON, which reads downstream as "the step published nothing". It refuses before writing, naming the byte count and the largest keys |
| a key that is not a string | JSON objects have string keys; `{1: "x"}` would come back as `{"1": "x"}` and the lookup would miss |
| `set()` after the file is written | writing twice means the second silently wins or silently does not, depending on the platform |

The truncation guard is the one that matters most, and it needs its twin on the
engine side: the engine has to tell **empty** from **does not parse**. Empty is a
step that published nothing, which is legitimate. Not parsing is an error whose
message must say *truncation*, because that is the cause in nine cases out of
ten.

### 5.3 Running outside Brevis

A fetcher run by hand has no `BREVIS_INPUT` and no `BREVIS_OUTPUT`.

`get()` returns the default, `set()` accepts and discards, and **the module says
so once** at the first call, at INFO:

```
brevis: not running under Brevis (BREVIS_OUTPUT is unset); context.set() is a no-op
```

Not an exception: a script that cannot be run by hand cannot be developed. Not
silent either: a `set()` that quietly did nothing in production is the same
failure this whole document is about, and the line is what tells the two apart.

### 5.4 Packaging

| | |
|---|---|
| import | `brevis` |
| PyPI | `brevis-context` — the noun `brevis` may not be free, and the qualified name says what it does |
| Python | **3.9+**. Data teams run old Pythons on managed clusters, and nothing here needs newer |
| dependencies | **none**, ever. It reads an env var and writes a file |
| layout | `sdk-python/` at the repository root, its own module, its own version, its own tag prefix `py/v*` |

Zero dependencies is a hard rule, not a preference. This package gets installed
into an image that already has pandas, pyarrow and a resolver at its limit; a
package that drags a transitive dependency into that is a package teams work
around instead of installing.

### 5.5 Testing it without an engine

The whole contract is an env var and a file path, so:

```python
def test_it_reads_what_the_previous_step_published(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"ingest": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("ingest.rows") == 42
```

No cluster, no database, no running engine. That property comes from the
contract being two strings, and it is the reason the contract is two strings.

---

## 6. The Go side moves too

The Go SDK gets the same two calls over the same contract, so a Go step and a
Python step exchange context without either knowing which wrote it.

It is the same package-level shape, and it must **not** grow into the Go SDK's
pipeline: `sdk.Context.Get` / `sdk.Context.Set`, reading the same env vars.
A consumer already using `sdk.Run` gets it for free; one using neither still
gets it, because it depends on nothing else in the module.

---

## 7. On the screen: the context on the React Flow node

The request asks for the context to be visible in the UI and on the graph, and
it is the reason the persistence is not optional. Once `task_runs.saida` holds
it, no new plumbing is needed — the graph endpoint already reads that table.

**On the node**, a count, because the card has room for one line and a JSON blob
would swallow it:

```
┌──────────────────────────────────────┐
│ ● extract              SDK v0.53.0   │
│   python fetch.py                    │
│   ⬤ Python                           │
│   1m02s · 2 published                │  ← new
└──────────────────────────────────────┘
```

**In the detail panel**, the object itself, keys sorted, values as written:

```
context published
  bucket      s3://landing/2026-09-07
  rows        48213
```

**On a node that reads**, the panel also shows where each value came from, which
is the question somebody actually has when a step behaves oddly:

```
context read
  extract.bucket    s3://landing/…   ← extract
```

Two rules the payload has to follow, both learned from the chips in
`2026-09-07-runtime-and-tooling-on-the-graph.md`:

- **A step that published nothing shows nothing.** No "0 published", no empty
  section. Most steps publish nothing and their card must not change.
- **The value is shown as it was written, never re-serialized for display.** A
  number that reads `48213` on the screen and `48213.0` in the next step's input
  is the class of difference this project has already paid for once, in
  `ingestion_id`.

And the warning belongs here rather than in a footnote: **this panel is why the
context is not a secret store.** Anyone who can see a run can see every value
published in it. §9 says it; the screen is where it becomes concrete.

---

## 8. How this is proven

**The cross-language end-to-end test is the acceptance criterion**, and nothing
below it counts.

One workflow, three steps, one run:

```yaml
steps:
  - id: ingest          # Go, using the Go SDK
    run: ./ingest
  - id: transform       # Python, using brevis-context
    run: python transform.py
    needs: [ingest]
  - id: publish         # neither: bash and jq
    run: ./publish.sh
    needs: [transform]
```

It asserts that `transform` reads what `ingest` wrote, that `publish` reads what
`transform` wrote through `jq`, and that all three outputs are in `task_runs`
afterwards. It runs against a real Postgres, through the process executor —
the same shape as the existing
`TestIntegrationStagesReachTheDatabaseThroughARealBinary`, which exists because
the stages feature shipped once with the whole path never exercised end to end.

Plus, per `CONTRIBUTING.md`, each of these with proof it bites:

- a 5 KB output is refused by the SDK **and** diagnosed by the engine as
  truncation, not read as empty;
- a resumed run gives the skipped step's output to the step that needs it;
- a failed attempt's output does not survive its retry;
- two parallel steps both publishing do not lose one another's keys;
- a step reading something it did not declare in `needs` gets an error saying to
  declare it — not `None`;
- a bare `get("bucket")` with no step is refused, naming the steps that
  published it.

---

## 9. What this is not

- **Not a message queue.** One step publishes, its dependents read. There is no
  fan-out, no subscription, no ordering beyond the DAG's.
- **Not state between runs.** §3.
- **Not a secret store.** The termination message is in the pod's status,
  readable by anyone with `get pods` in the namespace, and the persisted output
  is in the run history. The documentation says so on the field; the SDK cannot
  enforce it, because it has no way to know a string is a token.
- **Not the Go SDK in Python.** No drivers, no pagination, no checkpoints, no
  ingestion ids. If a Python team wants those, Python already has better ones,
  and Brevis's job is to run their code and carry a few kilobytes between steps.

---

## 10. Order of work

| | | unblocks |
|---|---|---|
| 1 | The engine's contract: `BREVIS_INPUT`, `BREVIS_OUTPUT`, reading the termination message | everything |
| 2 | `task_runs.saida` + assembling `BREVIS_INPUT` from it | resumed runs, and the request's "recover on failure" |
| ~~3~~ | ~~`needs:` in the YAML~~ — **dropped.** `depends_on` already declares it, and reading a step you do not depend on is a race against the scheduler rather than something to permit. A second field would be one more thing to keep in sync for no capability | |
| 4 | `sdk-python/`, the package, its tests | the community this is for |
| 5 | `sdk.Context` in the Go SDK | one contract, two languages |
| 6 | The cross-language end-to-end test | the proof it is not "works if you use Go" |
| 7 | The context on the node and in the detail panel | the request's "see it in the UI and on the React Flow" |
| 8 | `docs/CONTEXT.md`, and PyPI publishing in CI | somebody other than us can use it |

Steps 1–3 are the engine and are the majority of the work. Step 4 is perhaps two
hundred lines, most of them refusals, and it is the one the request is about —
which is the honest summary of this document: **the Python SDK is small because
the contract is the hard part, and the contract is not built yet.**
