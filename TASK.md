# What we are working on

The order is the one that was decided, and it is not the order of size. Each
line links the plan that carries the analysis; this file carries only what is
being attacked and where it stands.

| # | | plan | status |
|---|---|---|---|
| **1** | **Alerts and reports** — an `alert` pod, per-step alerting, and a scheduled insights report | [`plan/2026-09-08-alerts-and-reports.md`](docs/plan/2026-09-08-alerts-and-reports.md) | **done** |
| **2** | **Observability** — OpenTelemetry metrics for the engine, and `sdk.Meter` for consumers | [`plan/2026-09-08-observability.md`](docs/plan/2026-09-08-observability.md) | **done** |
| **3** | **Flow shapes** — `skipped`, trigger rules, edge labels, dynamic mapping, groups, sub-flows | [`plan/2026-09-08-flow-shapes.md`](docs/plan/2026-09-08-flow-shapes.md) | **done** |
| **4** | **Auto params** — the clock, the window and the lateness the engine hands every run | [`plan/2026-09-08-open-threads.md`](docs/plan/2026-09-08-open-threads.md) | **done** |
| **5** | **The scheduler's retry policy** — `--max-attempts` and `--retry-backoff`, and a default that is not inert | [`plan/2026-09-07-sdk-retry-policy-is-not-configurable.md`](docs/plan/2026-09-07-sdk-retry-policy-is-not-configurable.md) | **done** in `0.10.0` |
| **6** | **Schema evolution on load** — additive by default, lossy by opt-in, every change recorded | [`plan/2026-09-08-backlog.md`](docs/plan/2026-09-08-backlog.md) §10 | proposed |
| **7** | **Official task images** — `etl-go`, `etl-python`, `etl-node`, with a size gate | [`plan/2026-09-08-backlog.md`](docs/plan/2026-09-08-backlog.md) §7 | proposed |
| **8** | **Node.js context library** — the Python contract, in npm | [`plan/2026-09-08-node-context-sdk.md`](docs/plan/2026-09-08-node-context-sdk.md) | proposed |
| **9** | **The load trend screen** — the numbers every load already produces, kept and drawn | [`plan/2026-09-08-backlog.md`](docs/plan/2026-09-08-backlog.md) §11 | proposed |
| **10** | **Beyond Kubernetes** — Cloud Run, Lambda, ECS, EC2. Decide the return path first | [`plan/2026-09-08-backlog.md`](docs/plan/2026-09-08-backlog.md) §8 | proposed |

The two audits that produced this order:
[`plan/2026-09-08-open-threads.md`](docs/plan/2026-09-08-open-threads.md) — what
is open — and [`plan/2026-09-08-backlog.md`](docs/plan/2026-09-08-backlog.md) —
`NOTES.md` read against the tree.

## What the first four have to do with each other

They are not independent, and pretending they are is how two of them get built
twice.

```
        ┌──────────────────────────────────────────┐
        │  2. Observability                        │
        │     the metrics the engine emits         │
        └───────────┬──────────────────────────────┘
                    │ the INSIGHTS report reads them.
                    │ Without it, "infra insights" has no source.
                    ▼
    ┌──────────────────────────────┐
    │  1. Alerts and reports       │
    │     an outbox + a delivery   │
    └──────────────────────────────┘

    ┌──────────────────────────────┐      ┌───────────────────────────┐
    │  3. Flow shapes              │◄─────┤  context between steps    │
    │     a conditional reads a key│      │  (already built)          │
    │     a map reads a list       │      │                           │
    └──────────────────────────────┘      └───────────────────────────┘

    ┌──────────────────────────────┐
    │  4. Node.js                  │  depends on nothing: the contract exists
    └──────────────────────────────┘
```

**Where #2 stands.** The engine half has shipped: `/metrics` on its own port
from both processes, the queue, scheduler, run and step metrics, and
[`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md). `sdk.Meter` has shipped in the SDK too — the interface, the routing of the
numbers the SDK already counted, and a `pruning-check.sh` case proving a
consumer that declares one links a byte-for-byte identical dependency set.

What is left is `sdk/metrics/otelmeter`, and its order is forced rather than
chosen: it needs its own `go.mod`, and a sibling module here requires a
PUBLISHED SDK version and carries no `replace`. So the SDK carrying `Meter`
gets tagged first, and the module lands after. §5 of that plan has the
measurement that made a separate module necessary.

**#1 is done too.** The alerts outbox and `brevis alert`, `on_error:` in the
YAML, the alerts on the run's screen, and `brevis report` with its CronJob.

**The ordering constraint turned out to be half right.** The INSIGHTS report
wanted numbers from #2, and #2 shipped — as a Prometheus endpoint, not as rows
in Postgres. Rows, bytes and durations were always in the database and needed
nothing from #2; CPU and memory are in the collector, and for the engine to put
them in a weekly message it would have to become a metrics-backend client. So
the report carries the first three and says where the others live, which is the
final answer rather than a deferral. §6 of that plan records it.

**Split out of #3, and named so it is not rediscovered as a gap:** datasets and
data-aware scheduling — a step declaring it produces something, and a workflow
triggered when that something updates. It appeared in the target picture as the
`model_trained` node. It is a scheduling feature, not a graph one, and it gets
its own plan rather than riding along in #3.

The Node.js library depends on nothing: the contract it consumes has shipped
and is proven by a three-language test. It is not last because it is blocked —
it is behind #7 because shipping a library with no image to run it in delivers
half a feature.

**#5 was the one with somebody waiting**, and it shipped in `0.10.0`. It was
not a feature; it was a consumer report open since engine `0.7.0`. A run's
three attempts landed at 0s, 1s and 3s and neither number was reachable from
the CLI, which for a queue of HTTP fetches against rate-limited vendors is
indistinguishable from no retry at all. They now land at 0s, 30s and 1m30s and
both numbers are flags.

**#6 is next.** Schema evolution: the only item on this list that a running
consumer hits today, silently, whenever a vendor adds a field.

## Standing rules for all four

From `CONTRIBUTING.md`, restated because these four are where they will be
tested:

- **A test that would fail without the change**, and proof it bites — revert the
  change and watch the test go red.
- **A feature with a default gets a test that configures nothing.** Two features
  shipped switched off in one week, and both times every test set the field
  under test. See [`plan/2026-09-07-open-threads.md`](docs/plan/2026-09-07-open-threads.md).
- **A number that is always zero is worse than no number**, and a badge that can
  lie is worse than no badge. This applies hardest to #2.
- **English**, everywhere except the site's user-facing content.
