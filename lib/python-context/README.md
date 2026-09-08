# brevis

Pass context between the steps of a Brevis workflow, and read the clock the
engine already worked out for the run. That is all it does.

```bash
pip install brevis
```

```python
from brevis import context, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

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

## The clock this run should read

Every run also carries **auto params**: values the engine works out on its own.
Nobody declares them, every run has them, and they answer the question a
pipeline would otherwise answer with `datetime.now()`.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`run.now()` is the **slot** on a scheduled run. That matters more than it
sounds: a fetcher that reads `datetime.now()` and subtracts two hours is right
on a run that starts on time and forty minutes wrong on one the queue delayed —
and those forty minutes belong to *no run at all*, because the next slot reads
its own `now()` too. Nothing fails, and the gap is found weeks later.

```python
run.now()                   # the clock: the slot, or when the run started
run.window()                # (start, end), end excluded — or None
run.auto().date             # "2026-09-08": the partition, the folder, the WHERE
run.auto().delay_seconds    # how late this attempt was
run.auto().previous_error   # the run before this one did not succeed
run.context()               # id, attempt, trigger, params, and auto
```

`window()` comes from the **cron**, not from the history: a backfill of a slot
from March produces the window March had. Asking for `[start, end)` never
overlaps and never gaps, however late the run is and however often it retries.

It returns `None` rather than a pair of zero times when the workflow has no
schedule — a query from the epoch selects everything, and that failure should
not be silent. The `or` above is the whole fallback.

Run the script by hand and `run.now()` **is** the wall clock and `window()` is
`None`, so there is no branch to write for local development.

## One instance of a mapped step

Under `for_each:`, the engine runs one process per element and tells each one
which it got:

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

A JSON string arrives **without its quotes** — `for_each` over `["2026-01"]`
hands the step `2026-01`, not `"2026-01"`. Anything else — an object, a number,
a list — arrives as its JSON, so `json.loads(run.map_value())`.

`run.map_index()` is the position, and both are `None` on a step that is not
mapped. `None` and not `-1` or `""`: a sentinel is a number somebody eventually
does arithmetic on, and an empty string is indistinguishable from an element
that *is* the empty string.

A mapped step publishes **no context** downstream — four instances cannot share
one key — so a step that needs to hand something on writes a file, or a step
after it counts what landed.

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

## Versions

[`CHANGELOG.md`](CHANGELOG.md). This package versions on its own, separately
from the engine and from the Go SDK — a fix here does not force an engine
release. `brevis.run` needs an engine on `v0.9.0` or newer to have anything to
read; on an older one every field is empty and `run.now()` is the wall clock,
which is the same thing it does outside the engine.

## Testing your step

The whole contract is two environment variables, so there is nothing to mock:

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```
