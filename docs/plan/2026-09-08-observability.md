# Observability: OpenTelemetry metrics, and the ceiling they run into

**Written on** 2026-09-08 · **Base** engine `v0.7.0`, `sdk/v0.53.0`
**Status** proposed — not started · **TASK.md #2**

The engine has structured logs and a screen. It has **no metrics** — nothing a
Grafana, an alert rule or a capacity conversation can read.

The request is native metrics on the OTLP standard, scrapeable, plus
`sdk.Metrics` so a consumer's fetcher can emit its own.

Two constraints decide the whole design, and both were measured before anything
was written.

---

## 1. The measurement that comes first

| | packages | the engine would become | ceiling |
|---|---|---|---|
| the engine today | 299 | — | **330** |
| `+ go.opentelemetry.io/otel/sdk/metric` | +47 new | **346** | over |
| `+ the Prometheus exporter too` | | **393** | far over |
| `+ prometheus/client_golang` instead | +52 new | **351** | over |

Three things follow, and the third is the surprise.

**Any metrics library breaks the ceiling.** There is no light option: this is not
a case where careful shopping avoids the cost.

**OpenTelemetry is the *cheaper* of the two.** +47 against Prometheus's +52. So
the standard the request asks for is also the smaller dependency, and the trade
that looked like "standards versus weight" does not exist.

**So the ceiling moves, deliberately, and its meaning gets stated.** It is at 330
guarding against something specific — `engine-weight.sh` also asserts **no data
driver**, and that is the invariant with teeth: BigQuery, `aws-sdk-go` and
`jackc/pgx` must never be linked into the API pod through a stray import. A
package count is a proxy for that, and it is not the invariant itself.

The proposal: **raise the ceiling to 400, keep the driver check exactly as it
is, and write the reason into the script.** A number that moves whenever it is
inconvenient is not a gate — so it moves once, for a named reason, and the
commit says what was bought.

---

## 2. The constraint the SDK puts on this

`sdk/context` is 67 packages. `sdk/pycompat` is 68. The whole module is built so
that a fetcher reading a CSV never links BigQuery, and `pruning-check.sh` fails
if it does.

**`sdk.Metrics` importing OTel would add ~200 packages to every fetcher that
touches it**, and a fetcher that wants one counter would pay for the entire
telemetry stack.

### The shape that survives the pruning gate

The same one the module already uses for stores:

```go
// sdk — an interface, and nothing else. Zero new dependencies.
type Meter interface {
    Counter(name string, value int64, attrs ...Attr)
    Histogram(name string, value float64, attrs ...Attr)
}

// sdk/metrics/otel — the implementation, imported only by whoever wants it.
```

A fetcher that sets no `Meter` pays nothing and emits nothing. One that wants
OTel imports the subpackage, exactly as `from.Files` takes a `Store` rather than
knowing about S3.

`pruning-check.sh` gains a case: **a fetcher using the interface but not the
implementation must stay under 70 packages.** That is the assertion; the prose
above is only its explanation.

---

## 3. Push or scrape

The request says "scraping", which means pull, and OTel supports both.

**Serve a `/metrics` endpoint from the API pod, and let the collector scrape
it.** Reasons, in order:

- an OpenTelemetry Collector scrapes Prometheus format natively, so "OTLP
  standard" and "scrapeable" are not in tension;
- pull means the engine has no exporter endpoint to configure, no queue of
  undelivered points, and no second network dependency at run time;
- it degrades correctly: a collector that is down loses resolution, not data
  integrity, and the engine does not block.

Push over OTLP is a configuration option, not the default. The default is the
one that fails least badly.

**The scheduler needs its own endpoint**, and this is the part most designs get
wrong: the interesting numbers — queue depth, claim latency, slots in use — live
in the process that runs steps, not in the one that serves the UI. Two
deployments, two `/metrics`.

---

## 4. What to measure, and the rule for what not to

The rule this repository already states: **a number that is always zero is worse
than no number.** Every metric below is one somebody would act on.

### The queue and the scheduler — where the pain actually is

| metric | type | why it earns its place |
|---|---|---|
| `brevis_queue_depth{state}` | gauge | pending vs claimed. The first question when things feel slow |
| `brevis_claim_latency_seconds` | histogram | enqueued → claimed. This is the scheduler's real SLA |
| `brevis_slots_in_use` / `_limit` | gauge | the concurrency ceiling, which is otherwise invisible until it bites |
| `brevis_orphans_recovered_total` | counter | a worker died. Currently only a log line |

### Runs and steps

| metric | type | |
|---|---|---|
| `brevis_run_duration_seconds{workflow,status}` | histogram | |
| `brevis_step_duration_seconds{workflow,step,status}` | histogram | |
| `brevis_step_attempts_total{workflow,step}` | counter | flapping, which no screen shows well |
| `brevis_run_total{workflow,status,trigger}` | counter | |

### The cardinality rule, and it is not optional

**`run_id` must never be a label.** It is unbounded, and one unbounded label is
how a metrics backend becomes the most expensive part of an installation.

`workflow` and `step` are bounded by what has been published — a real bound, and
one worth asserting in a test rather than trusting: a check that the label set
comes from the definition and not from a run.

### What is deliberately not measured yet

**Infrastructure — CPU, memory, pod restarts.** The engine does not have it, and
the honest source is the cluster's own metrics, which a collector already
scrapes. Re-collecting it here would be inventing a worse `kube-state-metrics`.

`TASK.md` #1's INSIGHTS report wants these numbers; the right answer is that the
report reads them from the same place everything else does, not that the engine
grows a second collector.

---

## 5. `sdk.Metrics`, for the consumer

```go
sdk.Run(sdk.Pipeline{
    Meter: otelmeter.New(),          // opt-in, from sdk/metrics/otel
    Source: /* … */,
})
```

The SDK **already counts** what matters — rows in, rows out, pages, attempts,
duration per stage — and reports them in `Result` and in the `@brevis:` stage
lines. Metrics do not need new instrumentation; they need those existing numbers
routed to a `Meter` when one is set.

That is the important design point: **no consumer writes a counter to get the
standard metrics.** `sdk.Metrics` is for the numbers only their pipeline knows —
"rows rejected by the vendor", "quota remaining" — and everything the SDK
already knows arrives without asking.

---

## 6. Order of work

| | | |
|---|---|---|
| 1 | raise the ceiling to 400 with the reason written into `engine-weight.sh`, keeping the driver check | unblocks everything, and is one commit that can be reviewed on its own |
| 2 | `internal/observability/metrics`: the meter, the registry, `/metrics` on both binaries | the plumbing, with no metric yet |
| 3 | the queue and scheduler metrics | the ones with an operator waiting for them |
| 4 | run and step metrics | |
| 5 | `sdk.Meter` — the interface in the SDK, and the pruning case that pins its cost | the consumer's half, and it can be done in parallel with 3–4 |
| 6 | `sdk/metrics/otel` | |
| 7 | `docs/OBSERVABILITY.md` and a Grafana dashboard as JSON in `deployments/` | a metric nobody can find is a metric nobody uses |

Step 1 first and alone: it changes a gate, and a gate change buried in a feature
commit is one nobody reviews.

## 7. How it is proven

- **Every metric moves.** A test drives the code path and asserts the value
  changed — this is the concrete form of "no number that is always zero", and it
  is the assertion most metrics work skips.
- **A cardinality test**: build a metric set from a run and assert no label's
  value contains a UUID.
- **The pruning gate** measures a fetcher using `sdk.Meter` without the OTel
  implementation, and fails if it grows.
- **The engine weight gate** still fails on a data driver — that check is not
  weakened by the ceiling moving, and a test that adds a BigQuery import proves
  it.
- **`/metrics` with nothing configured returns a valid, empty-but-well-formed
  response**, rather than 404 or a panic. The default path is the one everybody
  hits first.

## 8. What this plan refuses

- **Traces, for now.** They are the more valuable half of OpenTelemetry for
  debugging a distributed run, and they are a bigger design: context propagation
  across a pod boundary, sampling, and a backend to send them to. Metrics first,
  because the questions above have no answer today and traces would answer a
  question nobody is currently asking.
- **A metrics endpoint on the task pods.** They are short-lived; scraping them
  is a race. Their numbers arrive through the SDK's stage protocol, which
  already works.
- **Re-collecting the cluster's own metrics.** See §4.
