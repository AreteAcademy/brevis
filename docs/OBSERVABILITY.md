# Observability

Brevis exposes Prometheus metrics on a port of its own, from **both** processes.
It has no traces yet, and [why not](#what-is-deliberately-absent) is at the
bottom.

```
GET http://<pod>:9090/metrics
```

## Two processes, two endpoints

This is the part most setups get wrong by scraping only the API.

| | serves | reports |
|---|---|---|
| `brevis serve` | the UI and the HTTP API | queue depth |
| `brevis scheduler` | nothing over HTTP | queue depth, claim latency, slots, orphans, runs, steps |

**The interesting numbers live in the scheduler.** It is the process that claims
work and runs steps; the API only serves screens. A dashboard built on the API's
scrape alone is a dashboard of an idle process.

## Why a separate port

`BREVIS_METRICS_ADDR` defaults to `:9090` and is deliberately not
`BREVIS_HTTP_ADDR`.

The HTTP port is the one behind the Ingress and behind `auth.Gate`. Putting
`/metrics` there leaves two options and both are wrong: inside the gate, no
scraper can reach it, because the session is a cookie set by a login form;
outside it, every workflow and step name is published to whoever finds the path.

On its own port it is never in the Ingress. Keep it that way — the port belongs
in a `ServiceMonitor` or a scrape annotation, not in an `Ingress` rule.

Set `BREVIS_METRICS_ADDR=""` to serve nothing. An **empty** value means off,
which is different from unset — unset gets the default.

## What is measured

### The queue and the scheduler

Where the pain actually is, and none of it was visible before.

| metric | type | |
|---|---|---|
| `brevis_queue_depth{state}` | gauge | `pending` vs `claimed`. The first question when things feel slow |
| `brevis_claim_latency_seconds` | histogram | enqueued to claimed. The scheduler's own SLA |
| `brevis_slots_in_use` / `brevis_slots_limit` | gauge | the concurrency ceiling, otherwise invisible until it bites |
| `brevis_orphans_recovered_total` | counter | a worker died. Until now this was a log line and nothing else |

`brevis_queue_depth` is read from the table at scrape time, not counted locally.
The queue is shared: several dispatchers write to it, and a local counter would
report one replica's opinion of a number that belongs to the queue.

### Runs and steps

| metric | type | labels | |
|---|---|---|---|
| `brevis_run_total` | counter | `workflow`, `status`, `trigger` | one per run, not one per attempt |
| `brevis_run_duration_seconds` | histogram | `workflow`, `status` | from the run's CREATION, so queue time is included |
| `brevis_step_duration_seconds` | histogram | `workflow`, `step`, `status` | one per ATTEMPT |
| `brevis_step_attempts_total` | counter | `workflow`, `step` | flapping shows here and nowhere else |

The two "per run" versus "per attempt" choices are the useful ones:

- **A run that fails twice and then succeeds is one success.** The dispatcher
  passes through the same code three times, and counting each pass would report
  three runs where the operator saw one.
- **A step that fails twice and then succeeds is three attempts**, two of them
  labelled `status="failed"`. That is the only place flapping is visible — the
  run is green, the screen is green, and the step has been failing every night
  for a month.

### The cardinality rule

**`run_id` is never a label**, and a test asserts no series carries anything
shaped like a UUID. One unbounded label is how a metrics backend becomes the
most expensive part of an installation.

`workflow` and `step` are bounded by what has been published, which is a real
bound. If you add a metric, keep it that way.

## Scraping it

The endpoint speaks the Prometheus text exposition format, which an
OpenTelemetry Collector scrapes natively — "OTLP standard" and "scrapeable" were
never in tension.

```yaml
# Pod annotations, for a Prometheus that discovers by annotation.
metadata:
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "9090"
    prometheus.io/path: "/metrics"
```

A failed scrape is a **500**, not an empty 200. If the queue depth cannot be
read because Postgres is unreachable, the target goes down rather than reporting
a queue that quietly looks calm.

## What is deliberately absent

**Infrastructure — CPU, memory, pod restarts.** The engine does not have these
numbers, and the honest source is the cluster's own metrics, which a collector
already scrapes. Re-collecting them here would be a worse `kube-state-metrics`.

**Metrics on the task pods.** They are short-lived and scraping them is a race.
Their numbers arrive through the SDK's `@brevis:` stage protocol, which already
works — see [`docs/SDK.md`](SDK.md).

**Traces.** They are the more valuable half of OpenTelemetry for debugging a
distributed run, and a bigger design: context propagation across a pod boundary,
sampling, and a backend to send them to. Metrics first, because the questions
above have no answer today.

**An OTLP push exporter.** It carries protobuf and gRPC, which is a larger
dependency than everything else in this feature put together. An installation
that needs push runs a Collector beside the engine and lets it scrape — which is
what a Collector is for.

## For contributors

The exposition format is written by hand in
`internal/observability/metrics/exposition.go` rather than imported. The
reasoning, with the measurements, is in
[`docs/plan/2026-09-08-observability.md`](plan/2026-09-08-observability.md) §1:
the OTel SDK costs 42 packages and its Prometheus exporter 49 more, 29 of them
`google.golang.org/protobuf`, to render a text format that has not changed in a
decade. `.github/scripts/engine-weight.sh` holds that decision down.

Two things in that file are easy to get wrong, and both have a test that fails
when the code is reverted:

- OpenTelemetry counts a histogram's buckets **per bucket**; Prometheus reads
  them as **running totals**;
- `le` holds a number inside a string, so sorting the output as text puts
  `le="+Inf"` first and `le="5"` after `le="3600"` — which Prometheus's own
  parser rejects.

That parser runs in the tests. It is a test-only dependency, so protobuf never
reaches the binary, and the format is checked by the reference implementation
rather than by belief.
