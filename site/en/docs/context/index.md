# Context between steps

> What one step tells the next — two environment variables and a file.

*https://brevis.sh/en/docs/context/ · brevis.sh docs (en)*

---

A step publishes values; the steps that depend on it read them. That is all the
feature does.

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

The key is always `<step id>.<name>`. A step reads what **its own dependencies**
published, and nothing else.

:::note What this is not
Not a message queue, not state between runs, and not a place to put data. It is
the note one step leaves for the next — a path, a watermark, a count.
:::

## Any language, no library required

The contract is **two environment variables and a file**, so a step with no SDK
at all takes part:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — what this step's dependencies published |
| `BREVIS_OUTPUT` | a path to write **one** JSON object to |

Under Kubernetes that path is `/dev/termination-log`, which the engine already
reads to get the exit code. So there is **no API call, no token and no port** —
and a library for it is a JSON parse and a file write.

| language | |
|---|---|
| Python | [`pip install brevis`](/en/docs/python/index.md) — it also carries `brevis.run`, with the [auto parameters](/en/docs/parameters/index.md) |
| Go | `sdk/context`, part of the SDK module, depends on nothing |
| anything else | the two variables above |

## Isolation

No step can overwrite what another published. Keys are prefixed with the id of
whoever wrote them, and the engine — not the step — decides that prefix. Two
steps publishing `bucket` produce `extract.bucket` and `load.bucket`, and
neither disappears.

## The 4096-byte ceiling

What a step publishes fits in **4096 bytes**. The limit is not arbitrary: under
Kubernetes the transport is `/dev/termination-log`, and that is what the kubelet
accepts.

Going over is a **step error**, with the message saying how many bytes were
written. The alternative would be truncation — and truncated JSON is invalid
JSON, discovered by the next step, which has no way of knowing the fault is not
its own.

If a value does not fit, it is not context: write it to a bucket and publish the
path.

## Retries and resumed runs

On a new attempt the step publishes again and the previous value is replaced. A
step that already succeeded neither re-runs nor republishes — what it said still
holds for those that depend on it.

## Running a step by hand

The two variables are the entire contract, so reproducing a run is exporting
them:

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## Next steps

- [Parameters](/en/docs/parameters/index.md) — what changes between runs, and the automatic ones
- [Step runtime](/en/docs/runtime/index.md) — what a step declares about where it runs
- [Python](/en/docs/python/index.md) — the client library, and the other languages
