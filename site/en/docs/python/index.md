# Python

> pip install brevis — context between steps, the run's clock and your pipeline's own metrics, with no dependencies at all.

*https://brevis.sh/en/docs/python/ · brevis.sh docs (en)*

---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**No dependencies, ever.** It reads one environment variable, writes one file
and prints one line to stdout. Requires Python 3.9+.

It is not a port of the Go SDK: no drivers, no pagination, no ingestion ids. It
exists so a team can use pandas, Polars or dbt and still get what an
orchestrator is for.

## Reading context

A key is always qualified by the step that published it:

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

`context.get("bucket")` with no step is **refused**, naming the steps that
published it. Steps are isolated — `extract` and `transform` can both publish
`bucket` without either losing it — and a bare key throws that away the moment
two of them do. Searching and picking one is precedence by accident: it works
until it silently does not.

Reading a step this one does not depend on is an error, and the message points
at the fix.

## Writing

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

Calls **merge**. The same key twice keeps the last write; a different key leaves
the first alone. **There is no step argument** — the only namespace a process
can write is its own, which is what makes the isolation structural rather than a
rule.

Nothing reaches the disk until the process exits, so a step that dies hard
publishes nothing. That is correct: a crashed step's output described work that
did not finish.

## The clock this run should read

Every run also carries **auto parameters**: values the engine works out on its
own. Nobody declares them, every run has them, and they answer the question a
pipeline would otherwise answer with `datetime.now()`.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning Why not `datetime.now()`
`run.now()` is the **slot** on a scheduled run. A fetcher that reads
`datetime.now()` and subtracts two hours is right on a run that starts on time
and forty minutes wrong on one the queue delayed — and those forty minutes
belong to **no run at all**, because the next slot reads its own `now()` too.
Nothing fails, and the gap is found weeks later.
:::

| | |
|---|---|
| `run.now()` | the clock: the slot, or when the run started |
| `run.window()` | `(start, end)`, end excluded — or `None` |
| `run.auto()` | the auto parameters, below |
| `run.context()` | id, attempt, trigger, params, and auto |
| `run.params()` · `run.param(name)` | the parameters declared in the workflow |
| `run.map_index()` · `run.map_value()` | the instance, under `for_each:` |

`window()` comes from the **cron**, not from history: a backfill of a slot from
March produces the window March had. Asking for `[start, end)` never overlaps
and never gaps, however late the run is and however often it retries.

It returns `None` rather than a pair of zero times when the workflow has no
schedule — a query from the epoch selects everything, and that failure should
not be silent. The `or` above is the whole fallback.

### The auto parameters

| field | |
|---|---|
| `date` | `"2026-09-08"` — the partition, the folder, the `WHERE` |
| `adjusted_at` | the clock to read instead of `now()` |
| `scheduled_at` | the slot the cron asked for; `None` on a manual run |
| `started_at` | when **this attempt** began; a retry moves it |
| `delay_seconds` | how late this attempt was; never negative |
| `interval_start` · `interval_end` | the window, end excluded |
| `previous_error` | the run before this one did not succeed |
| `previous_success_at` | the slot of the last one that **did** — what makes `previous_error` actionable |

Run the script by hand and `run.now()` **is** the wall clock and `window()` is
`None`, so there is no branch to write for local development.

## One instance of a mapped step

Under [`for_each:`](/en/docs/workflows/index.md), the engine runs one process per element
and tells each one which it got:

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

A JSON string arrives **without its quotes** — `for_each` over `["2026-01"]`
hands the step `2026-01`, not `"2026-01"`. Anything else — an object, a number,
a list — arrives as its JSON, so `json.loads(run.map_value())`.

Both are `None` on a step that is not mapped. `None`, and not `-1` or `""`: a
sentinel is a number somebody eventually does arithmetic on, and an empty string
is indistinguishable from an element that *is* the empty string.

A mapped step publishes **no context** downstream — four instances cannot share
one key. A step that needs to hand something on writes a file, or a step after
it counts what landed.

## Metrics only your pipeline knows

The engine already measures what it can see: duration, attempts, queue depth.
What it has no way of knowing is how many rows landed, how many the vendor
rejected, or how many bytes were discarded. The step knows that, and the library
carries it.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

They come out of the **engine's** `/metrics`, on the scheduler's port, labelled
with the workflow and the step:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | a gauge. After the step exits the value stays until the next run writes another |
| `metrics.inc(name, by=1)` | a counter. Adds up across steps and across runs |

### Why this does not open a port

Because **a step is not scrapeable**. `:9090` belongs to the scheduler and the
API — long-lived processes with a stable address, scraped every fifteen to sixty
seconds. A step starts in its own pod, runs for forty seconds and exits: a port
it opened would be scraped never, or once by luck.

The three known answers to that are a Pushgateway (a component to operate, and
series that stay until somebody deletes them), an OTLP collector (a component
**and** a dependency in this library, which has none and will not get one), or
letting the **orchestrator** carry them. Brevis already reads every step's
stdout looking for `@brevis:` lines — that is how the SDK's phases reach the
graph — and it already has a meter and a Prometheus endpoint.

So the library writes **one line to stdout** and the engine does the rest. The
metric arrives labelled with the workflow and the step for free, because the
engine knows what it was running; through a Pushgateway this library would have
to label itself, and would get it wrong.

The write is `print(flush=True)`, because the engine reads that pipe **live**. A
buffered line arrives when the process exits — which for a step that runs for an
hour is an hour late, and for a step that is killed, never.

### What a metric refuses

**An invalid name is refused, loudly.** Prometheus accepts letters, digits and
underscore, and the first character cannot be a digit. A name it refuses does
not produce a broken metric — it produces an exposition it **refuses to parse**,
and the **whole scrape** is lost, every other metric with it. So `rows-loaded`
raises `MetricError` here, where the mistake was made, rather than arriving
under a name nobody wrote.

| refused | because |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | Prometheus will not parse it, and the whole scrape goes |
| `metrics.set("ok", True)` | `bool` is an `int` in Python, and reporting `1` is a number whose meaning depends on knowing that. Report the count or the duration, not the state |
| `metrics.inc("x", -1)` | a counter only goes up. Every `rate()` over it assumes so, and the breakage shows up in a graph weeks later |

Run the script by hand and the line still goes to stdout, where it reads as what
it is. Nothing collects it and nothing fails — the same as `context.set()` on a
laptop.

## What it refuses

| | because |
|---|---|
| more than 4096 bytes | the platform **truncates** rather than refuses, so an oversized object arrives cut in half and reads downstream as "published nothing" |
| a value that is not JSON | a `datetime` becomes `"..."` or vanishes depending on who serializes it. The error names the key and suggests `.isoformat()` |
| a non-string key | `{1: "x"}` comes back as `{"1": "x"}` and the lookup misses |

The ceiling is the platform's number, not ours. Every "context between tasks"
feature fails the same way — somebody puts **data** where only **context**
fits. Four kilobytes hold a watermark, a count or a list of partitions. Publish
a reference and leave the rows where they are.

:::danger This is not a secret store
Everything published is visible to anyone who can see the run — it is in the
pod's status and in the run history. Publish a path, not a signed URL.
:::

## Running it by hand

Outside Brevis there is no input and nothing reads the output. `get()` returns
the default, `set()` accepts and discards, and the library says so once at INFO.
It **does not raise**: a script that cannot be run by hand cannot be developed.
The validation still runs, so a value that works on your laptop works in
production.

## Testing your step

The whole contract is two environment variables, so there is nothing to mock:

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## Versions

The library versions on its own, separately from the engine and from the Go SDK.

:::warning Do not use `0.2.0`
It shipped wheel-only on PyPI. `0.2.1` is the same code, complete.
:::

`brevis.run` needs an engine on **`v0.9.0` or newer** to have anything to read;
on an older one every field is empty and `run.now()` is the wall clock — the
same thing it does outside the engine.

## Next steps

- [Client libraries](/en/docs/libraries/index.md) — the contract and the other languages
- [Context between steps](/en/docs/context/index.md) — the concept
- [Parameters](/en/docs/parameters/index.md) — the declared ones
