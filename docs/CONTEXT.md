# Context between steps

What one step tells the next.

```yaml
steps:
  - id: extract
    run: python fetch.py
  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

That is the whole feature. It is not a message queue, not state between runs,
and not a place to put data.

## Any language, and no library required

The contract is two environment variables and a file, so a step with no SDK
takes part:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — what the steps this one depends on published |
| `BREVIS_OUTPUT` | a path to write one JSON object to |

Under Kubernetes that path is `/dev/termination-log`, which the engine already
reads to get the exit code. So there is **no API call, no token and no port**,
and a library for it is a JSON parse and a file write.

| language | |
|---|---|
| Python | [`lib/python-context`](../lib/python-context/) — `pip install brevis`; it also carries `brevis.run`, the run's [auto params](https://brevis.dev/docs/parameters/#auto-params) |
| Go | [`sdk/context`](../sdk/context/) — part of the SDK module, depends on nothing |
| anything else | the two variables above |

## How it actually reaches the database

The library does **not** talk to the API. There is no `BREVIS_HOST` to set and
nothing to resolve. The step's process opens no socket.

What happens instead, with a step running in its own pod:

```
1. scheduler   builds the pod, and sets on the container:
                 env  BREVIS_INPUT  = {"extract": {...}}      ← a value
                 env  BREVIS_OUTPUT = /dev/termination-log    ← a path
                 terminationMessagePath: /dev/termination-log

2. your pod    context.get(...)  reads an environment variable
               context.set(...)  writes that file
               ── no network at all ──

3. kubelet     the container ends, and the kubelet copies that file into
               the pod's status, as containerStatuses[].state.terminated.message

4. scheduler   was ALREADY reading that status to get the exit code.
               The context arrives on the same call.

5. scheduler   writes it to Postgres: task_runs.saida

6. API pod     reads it from Postgres when somebody opens the run screen
```

**Nobody pushes the context anywhere. The engine collects it.**

The two processes that do talk to something are the ones that already did: the
scheduler to the Kubernetes API, for a status it was fetching anyway, and the
API pod to Postgres. Your step talks to a file.

That is why the library has no dependencies, why it needs no credential, why
the task pod's ServiceAccount still has no permission in the cluster, and why
`brevis run` works on a laptop with no server at all — there the path is a
temporary file the runner reads instead of a termination message.

The 4096-byte ceiling comes from this path too: the kubelet truncates that
message at exactly that size.

## Isolation: no step can overwrite another

Context is keyed by the step that published it, and a step writes only its own
namespace — `set()` takes no step argument in either library. `extract` and
`transform` can both publish `bucket` and neither loses it.

Reading is therefore always qualified:

```python
context.get("extract.bucket")     # yes
context.get("bucket")             # refused, naming the steps that published it
```

A bare key is refused rather than searched. Searching and picking one is
precedence by accident: it works until a second step publishes the same name,
and then somebody spends an afternoon on a value that came from the wrong step.

## Who can read what

**`depends_on` is the scope**, transitively. A step sees what it depends on and
what those depend on, and nothing else.

```
extract ──> transform ──> load
```

`load` sees `transform` and `extract`, without `transform` re-publishing
anything. Two steps running in parallel do not see each other — reading a step
you do not depend on would be a race against the scheduler, so it is not
permitted rather than merely discouraged.

There is no `needs:` field to declare. The dependency edge already says it.

## The 4096-byte ceiling

A step may publish 4096 bytes, and the number is the platform's rather than
ours: the container's termination message is truncated at exactly that.

Inheriting a ceiling is stronger than enforcing a policy. Every "context between
tasks" feature fails the same way — somebody puts **data** where only **context**
fits, and the orchestrator's database becomes the pipeline's bottleneck.

Four kilobytes hold a watermark, a count, a list of partitions. **Publish a
reference and leave the rows where they are.**

Going over is refused before anything is written, naming what is holding the
bytes. It has to be, because the platform *truncates* rather than refuses: an
oversized object would arrive cut mid-string and read downstream as "this step
published nothing".

## Retries and resumed runs

**A step that fails and re-runs replaces its own output.** The previous
attempt's output described work that did not finish.

**A run that resumes reads it back from the database.** The engine skips a step
that already succeeded, and the step below still gets what it published — which
is why the context is stored in `task_runs.saida` and not held in memory.

## On the screen

Open a run and click a step. The panel answers two different questions.

**What this step published** — the values it handed to the steps below:

```
context.bucket      s3://landing/2026-09-07
context.rows        48213
```

**What was available to it** — what the engine put in its `BREVIS_INPUT`, with
the step that wrote each value:

```
available extract.bucket    s3://landing/2026-09-07   ← extract
available transform.rows    47998                     ← transform
```

The card itself carries only a count, because it has room for one line.

The second section says **available**, not "read", and the difference is not
pedantry: the engine does not observe `get()` calls. It knows what it handed the
step, not what the code asked for — a step can be given a value it never
touches. Calling it "read" would be a claim the data does not support.

Both sections are absent when there is nothing to say, so a step with no
dependencies that publishes nothing looks exactly as it did before any of this
existed.

Values are rendered **as they were written**: a number stays a number.

**That panel is why this is not a secret store.** Everything published is
visible to anyone who can see the run — it is in the pod's status and in the run
history. Publish a path, not a signed URL. No SDK can enforce this, because none
of them can tell that a string is a token.

## Running a step by hand

Outside Brevis there is no input and nothing reads the output. Reads return the
default, writes are accepted and discarded, and the Python library says so once
at INFO. Neither raises: a step that cannot be run by hand cannot be developed.

The validation still runs in both, so a value that works on a laptop works in
production.

## What this is not

- **Not state between runs.** Yesterday's watermark is a different feature with
  different questions — where it lives, who reads it, what happens when two runs
  overlap. Blurring the two turns the context into a database nobody designed.
  Today the answer is the table your pipeline already writes to.
- **Not a message queue.** One step publishes; its dependents read. No fan-out,
  no subscription, no ordering beyond the DAG's.
- **Not a way to move data.** See the ceiling.
