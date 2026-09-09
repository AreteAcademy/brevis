# Observability

> Prometheus metrics from both processes, what to watch, and what is deliberately absent.

*https://brevis.sh/en/docs/observability/ · brevis.sh docs (en)*

---

Brevis exposes Prometheus metrics on a port of its own, from **both** processes.

```
GET http://<pod>:9090/metrics
```

The port comes from `BREVIS_METRICS_ADDR`. It is separate from the HTTP port on
purpose: metrics do not go through the interface's authentication, and serving
both from one place would force a choice between an authenticated scrape and an
open dashboard.

## Two processes, two endpoints

This is the part most setups get wrong, by scraping only the API.

| | serves | reports |
|---|---|---|
| `brevis serve` | the UI and the HTTP API | queue depth |
| `brevis scheduler` | nothing over HTTP | queue depth, claim latency, slots, orphans, runs, steps |

The scheduler is what executes, so nearly everything comes from it. A dashboard
built only on the API shows the queue growing and **nothing** about what is
draining it.

## The metrics a step publishes

Beyond what the engine measures, a step can publish its own — how many rows
landed, how many the vendor rejected:

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

They show up on the **same** scheduler `/metrics`, prefixed and labelled by the
engine:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

The step opens no port: it writes a line to stdout and the engine records it — a
pod that lives forty seconds is not scrapeable. See [Python](/en/docs/python/index.md).

## What to watch

| signal | why |
|---|---|
| queue depth rising without falling | the scheduler died, or `--concurrency` is low for the load |
| claim latency growing | contention on the database, or a queue too large for the interval |
| orphans > 0 | runs claimed by a process that died before finishing |
| slots not materialised | the scheduler loop stopped, and schedules stop with it |

The first two alone do not tell "high load" from "dead process" — which is why
slots matter. A queue rising **with** slots being created is load; a queue rising
**without** slots is a stopped loop.

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

With the Prometheus Operator, one `PodMonitor` per role — and it does need to be
one per role, because the two Deployments carry different labels and report
different sets.

## What is deliberately absent

**Traces.** There is no OpenTelemetry yet. A step runs as its own pod, and what
matters about it — start, end, attempt, exit code, log — is already in the
database and on the screen. The useful trace would be the one inside the step,
and that belongs to the step's code, not to the orchestrator.

That holds while the step is the unit. The day a step calls another service and
the question becomes "where did the 40 seconds go", the trace starts being worth
what it costs.

## Next steps

- [Kubernetes](/en/docs/kubernetes/index.md) — deploying both processes
- [Configuration](/en/docs/configuration/index.md) — `BREVIS_METRICS_ADDR` and the rest
