# A step's own metrics, on the engine's `/metrics`

**Written on** 2026-09-09 · **Base** engine `v0.11.2`, `sdk/v0.57.0`, `brevis` (py) `0.2.1`
**Status** proposed

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)
metrics.inc("vendor_rejected_total")
```

---

## 1. Why port 9090 cannot be the answer

`:9090` belongs to the **scheduler and the API**: long-lived processes with a
stable address, scraped every fifteen to sixty seconds.

A step is the opposite. It starts in its own pod, runs for forty seconds and
exits. A step that exposed a scrape endpoint would be scraped **never**, or once
by luck. This is the short-lived-job problem, and it has three known answers:

| | cost |
|---|---|
| **Pushgateway** | a component to operate, and series that stay until somebody deletes them |
| **OTLP to a collector** | a component **and** a dependency in the step's image. The engine already refused the OTLP exporter on weight -- 49 packages, 29 of them protobuf. See [`2026-09-08-observability.md`](2026-09-08-observability.md) §1 |
| **the orchestrator carries them** | nothing new |

## 2. The third one is already built

Two mechanisms exist and meet exactly here:

- The engine **already reads every step's stdout** looking for `@brevis:` lines.
  That is how the SDK's phases reach the graph, and `stageCollector` does it for
  the local executor and the pod alike -- it does not know which produced the
  line.
- The engine **already has a meter and a Prometheus exposition** on `:9090`.

So the step declares and the engine carries, which is the same shape
`context.set()` already has. The metric arrives labelled with the **workflow and
the step** for free, because the engine knows what it was running; through a
Pushgateway the step would have to label itself, and would get it wrong.

```
step stdout   @brevis:{"type":"metric","name":"rows_loaded","value":48213,"kind":"gauge"}
      ↓        stageCollector, the loop that already reads this pipe
engine meter  brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
      ↓
:9090         the scrape that already exists
```

Nothing new to run, no dependency in the Python library -- which has none and
will not get one.

## 3. The four decisions

### Cardinality, which is the one that kills a process

The metric's NAME is user input. `metrics.set(f"rows_{customer}", n)` in a loop
grows the engine's registry without bound until the process dies -- and it dies
in the scheduler, taking the runs with it.

So there is a **ceiling on distinct names**, per process, and past it the value
becomes a log line rather than a series. `stageCeiling = 60` is the precedent
and its comment is the argument: *"the log stream becomes a database write here
... this is the cap of somebody who does not trust what came down the pipe."*

The ceiling is on NAMES, not on writes: a name already registered costs nothing
to write again.

### An invalid name is REFUSED, not normalised

Prometheus names match `[a-zA-Z_:][a-zA-Z0-9_:]*`. A `minha-metrica` with a
hyphen does not produce a broken metric -- it produces an exposition Prometheus
**refuses to parse**, and the whole scrape is lost, every other metric with it.

Normalising in silence would make the series appear under a name nobody wrote,
which is worse: the metric is missing and the reason is invisible. It is refused
in the LIBRARY, with the rule in the message, so the mistake is found where it
was made.

### `set` is a gauge, and a gauge from a dead process is a frozen value

After the step exits, `rows_loaded` keeps its last value until the next run
writes another. For *"how many rows did the last run load"* that is exactly
right, and it is what people ask for.

For *"how many rows this month"* it is wrong, and that is what `inc` is for:

| | | |
|---|---|---|
| `metrics.set(name, v)` | gauge | last value wins, per workflow and step |
| `metrics.inc(name, n=1)` | counter | adds up across runs |

Both carry the same labels, and neither takes the run's id -- one unbounded
label is how a metrics backend falls over, and the engine's own metrics already
refuse it for the same reason.

### The Go SDK gets the same emitter

`sdk.Meter` exists and today the consumer has to supply an implementation, so a
fetcher that counts something publishes it nowhere unless it wires
`sdk/metrics/otelmeter` and runs a collector.

A stdout meter makes `sdk.Meter` work with **no configuration**, and makes Go and
Python behave identically -- which matters, because the alternative is two
answers to "where did my metric go" depending on the language.

## 4. What this deliberately is not

- **Not a general metrics client.** No histograms from a step: a histogram is a
  set of buckets, and buckets chosen per step by whoever wrote the step is a
  cardinality decision made in the wrong place.
- **Not labels from the step.** The engine supplies `workflow` and `step`. A
  step that could add its own would add `customer_id` on the first Tuesday.
- **Not a replacement for the SDK's own numbers.** Rows, bytes and durations are
  already recorded by `sdk.Meter` and already reach the run's screen; this is
  for what only the pipeline knows.

## 5. How it is proven

The exposition is the part that fails silently -- an invalid name loses the
whole scrape, not one series. The engine's metrics package already tests against
**Prometheus's own parser** (`prometheus/common/expfmt`, a test-only
dependency), which is what caught the `le="+Inf"` ordering bug. A step metric
with a hostile name goes through that same parser in a test.

And the path end to end: a real process writing a real `@brevis:` line to a real
pipe, through the local executor, arriving on the exposition. `stages.go` has
that test already; this adds a metric to it.
