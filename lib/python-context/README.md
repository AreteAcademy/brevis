# brevis

Pass context between the steps of a Brevis workflow. That is all it does.

```bash
pip install brevis
```

```python
from brevis import context

bucket = context.get("extract.bucket")

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
```

**No dependencies, ever.** It reads one environment variable and writes one
file. There is no API call, no token and no port: the engine hands a step its
input when it starts it and reads the output back when it ends.

This is **not** a port of the Go SDK. No drivers, no pagination, no ingestion
ids — Python already has better tools for those, and this exists so a team can
use pandas, Polars or dbt and still get the one thing an orchestrator is for.

## Reading

A key is always qualified by the step that published it:

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
```

`context.get("bucket")` with no step is **refused**, naming the steps that
published it. Steps are isolated — `extract` and `transform` can both publish
`bucket` without either losing it — and a bare key throws that away the moment
two of them do. Searching and picking one is precedence by accident: it works
until it silently does not.

Reading a step this one does not `depends_on` is an error pointing at the fix.

## Writing

```python
context.set(name="Daniel")
context.set(label="Nome")
# both survive: {"name": "Daniel", "label": "Nome"}
```

Calls **merge**. Setting the same key twice keeps the last write; setting a
different key leaves the first alone. There is no step argument — the only
namespace a process can write is its own, which is what makes the isolation
structural rather than a rule.

Nothing reaches the disk until the process exits, so a step that dies hard
publishes nothing. That is correct: a crashed step's output described work that
did not finish.

## What it refuses

| | because |
|---|---|
| more than 4096 bytes | the platform **truncates** rather than refuses, so an oversized object arrives cut in half and reads downstream as "published nothing" |
| a value that is not JSON | a `datetime` becomes `"..."` or vanishes depending on who serializes it. The error names the key and suggests `.isoformat()` |
| a non-string key | `{1: "x"}` comes back as `{"1": "x"}` and the lookup misses |

The ceiling is the platform's number, not ours. Every "context between tasks"
feature fails the same way — somebody puts **data** where only **context** fits.
Four kilobytes hold a watermark, a count or a list of partitions. Publish a
reference and leave the rows where they are.

## Running it by hand

Outside Brevis there is no input and nothing reads the output. `get()` returns
the default, `set()` accepts and discards, and the library says so once at INFO.
It does not raise: a script that cannot be run by hand cannot be developed. The
validation still runs, so a value that works on your laptop works in production.

## This is not a secret store

Everything published is visible to anyone who can see the run — it is in the
pod's status and in the run history. Publish a path, not a signed URL.

## Testing your step

The whole contract is two environment variables, so there is nothing to mock:

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```
