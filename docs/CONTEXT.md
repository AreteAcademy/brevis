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
| Python | [`lib/python-context`](../lib/python-context/) — `pip install brevis-context` |
| Go | [`sdk/context`](../sdk/context/) — part of the SDK module, depends on nothing |
| anything else | the two variables above |

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

A step that published something shows a count on its card, and the values in the
detail panel, keys sorted, each rendered as it was written.

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
