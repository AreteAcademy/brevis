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

Re-measured on 2026-09-07 against the tree as it stands, because a ceiling moved
on last week's number is the same mistake the ceiling exists to prevent. These
are unions with the engine's own 299, not standalone counts — most of a
standalone count is stdlib the engine already has.

| the engine would compile | packages | ceiling 330 |
|---|---|---|
| today | **299** | under |
| `+ otel/sdk/metric`, manual reader | **341** (+42) | over |
| `+ the Prometheus exporter as well` | **388** (+89) | far over |

**Any metrics library breaks the ceiling.** There is no light option, and that
much the first draft had right.

### What the first draft got wrong: the exporter is half the cost

The +89 splits very unevenly, and the split is the whole design decision:

| | packages |
|---|---|
| `otel/sdk/metric` — the instruments, the aggregation, the manual reader | **42** |
| `exporters/prometheus` on top of it | **49** |

And of the exporter's 49, **29 are `google.golang.org/protobuf`** — pulled in
because `client_golang` registers through `client_model`, which is generated
protobuf. The engine does not otherwise contain protobuf, and dragging it in to
render forty lines of text is exactly the transitive drag `engine-weight.sh`
exists to catch.

**So the exporter does not ship, and the exposition is written here.** The
Prometheus text format is `name{label="value"} 42`, plus `_bucket` / `_sum` /
`_count` for a histogram. It has not changed in a decade, OpenMetrics is a
superset of it, and rendering it from a `metricdata.ResourceMetrics` is around a
hundred lines.

Three things come with owning it, and only the first is about weight:

- **the output is exactly what §4 designs.** The official exporter adds
  `otel_scope_name` and `otel_scope_version` labels to every series and a
  `target_info` metric — real cardinality that installations routinely turn off;
- **`/metrics` becomes testable as bytes.** A golden test on the exposition is
  worth more than a test that asserts a scrape succeeded;
- **the two places it is easy to get wrong are named up front** and get tests of
  their own: label-value escaping, and the fact that OTel reports a histogram's
  buckets **per bucket** while Prometheus wants them **cumulative**.

What is given up is a maintained edge-case implementation. That is a real cost,
and it is accepted because the edge cases are ones this engine controls: it
names its own metrics, so nothing needs sanitising.

### The OTel SDK does ship

Dropping OTel as well and hand-rolling counters would answer a different
question than the one asked, and it would trade 42 packages of tested
concurrent aggregation for two hundred lines that are subtly wrong under load.
It also keeps the door open: the engine speaks OTel internally, so an
installation that wants OTLP push adds a reader rather than a rewrite.

### So the ceiling moves — to 360, not to 400

It is at 330 guarding something specific. `engine-weight.sh` also asserts **no
data driver**, and that is the invariant with teeth: BigQuery, `aws-sdk-go` and
`jackc/pgx` must never be linked into the API pod through a stray import. The
package count is a proxy for that, and it is not the invariant itself.

**Raise it to 360, keep the driver check untouched, and add protobuf and
`client_golang` to the forbidden list.** 360 is the measured 341 plus the
handful of packages this work adds, plus about five percent — enough that a
patch bump upstream does not turn CI red, tight enough that the next library
still has to argue for itself.

Adding protobuf to the forbidden list is what makes this decision hold: without
it, someone reaches for the exporter in six months, the count lands under 360,
and the gate says nothing.

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

The request says "scraping", which means pull, and the design in §1 settles it:
a manual reader is a pull reader. `Collect` runs when `/metrics` is requested,
and nothing accumulates between scrapes waiting to be shipped.

- an OpenTelemetry Collector scrapes Prometheus format natively, so "OTLP
  standard" and "scrapeable" were never in tension;
- pull means the engine has no exporter endpoint to configure, no queue of
  undelivered points, and no second network dependency at run time;
- it degrades correctly: a collector that is down loses resolution, not data
  integrity, and the engine does not block.

**Push over OTLP is not built in**, and the reason is now measurable rather than
a preference: the OTLP exporter carries protobuf and gRPC, which is a bigger
dependency than everything else in this plan put together. An installation that
needs push runs a Collector next to the engine and lets it scrape — which is
what a Collector is for.

**The scheduler needs its own endpoint**, and this is the part most designs get
wrong: the interesting numbers — queue depth, claim latency, slots in use — live
in the process that runs steps, not in the one that serves the UI. Two
deployments, two `/metrics`.

**`/metrics` is not behind the login.** `auth.Gate` protects the UI because the
UI triggers pipelines; a scrape endpoint that needs a session cannot be scraped
by anything normal. It carries no run ids, no parameters and no logs — §4's
cardinality rule is also what makes it safe to leave open — and it binds to the
same address as the rest, so an installation that wants it private handles that
with a NetworkPolicy, the way it already handles the database.

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
| 1 | raise the ceiling to 360 with the reason in `engine-weight.sh`, keep the driver check, and forbid protobuf and `client_golang` | unblocks everything, and is one commit that can be reviewed on its own |
| 2 | `internal/observability/metrics`: the registry over `otel/sdk/metric`, with a manual reader | the plumbing, with no metric yet |
| 3 | the Prometheus exposition, and its golden test | the ~100 lines bought in §1, proven before anything depends on them |
| 4 | `/metrics` on both `serve` and `scheduler` | two processes, two endpoints |
| 5 | the queue and scheduler metrics | the ones with an operator waiting for them |
| 6 | run and step metrics | |
| 7 | `sdk.Meter` — the interface in the SDK, and the pruning case that pins its cost | the consumer's half, and it can be done in parallel with 5–6 |
| 8 | `sdk/metrics/otel` | |
| 9 | `docs/OBSERVABILITY.md` and a Grafana dashboard as JSON in `deployments/` | a metric nobody can find is a metric nobody uses |

Step 1 first and alone: it changes a gate, and a gate change buried in a feature
commit is one nobody reviews.

Step 3 before step 4 is deliberate. The exposition is the part with no library
behind it, so it gets written and proven against known input before an HTTP
handler makes it look finished.

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
- **The exposition is asserted byte for byte** against a known metric set —
  which is the point of owning it rather than importing it.
- **A histogram's buckets come out cumulative.** OTel counts per bucket and
  Prometheus reads them as running totals; getting this backwards produces a
  chart that looks plausible and is wrong, which is worse than one that breaks.
- **A label value containing a quote, a backslash and a newline survives the
  round trip**, because that is the other half of what an exporter would have
  done for us.

## 8. What this plan refuses

- **Traces, for now.** They are the more valuable half of OpenTelemetry for
  debugging a distributed run, and they are a bigger design: context propagation
  across a pod boundary, sampling, and a backend to send them to. Metrics first,
  because the questions above have no answer today and traces would answer a
  question nobody is currently asking.
- **A metrics endpoint on the task pods.** They are short-lived; scraping them
  is a race. Their numbers arrive through the SDK's stage protocol, which
  already works.
- **An OTLP push exporter in the engine.** See §3: it costs protobuf and gRPC,
  and a Collector already does this job from the outside.
- **Re-collecting the cluster's own metrics.** See §4.
