# What we are working on

The order is the one that was decided, and it is not the order of size. Each
line links the plan that carries the analysis; this file carries only what is
being attacked and where it stands.

| # | | plan | status |
|---|---|---|---|
| **1** | **Alerts and reports** — an `alert` pod, per-step alerting, and a scheduled insights report | [`plan/2026-09-08-alerts-and-reports.md`](docs/plan/2026-09-08-alerts-and-reports.md) | proposed |
| **2** | **Observability** — OpenTelemetry metrics for the engine, and `sdk.Meter` for consumers | [`plan/2026-09-08-observability.md`](docs/plan/2026-09-08-observability.md) | **built**, waiting on an SDK tag for `otelmeter` |
| **3** | **Flow shapes** — `skipped`, trigger rules, edge labels, dynamic mapping, groups, sub-flows | [`plan/2026-09-08-flow-shapes.md`](docs/plan/2026-09-08-flow-shapes.md) | proposed |
| **4** | **Node.js context library** — the Python contract, in npm | [`plan/2026-09-08-node-context-sdk.md`](docs/plan/2026-09-08-node-context-sdk.md) | proposed |

## What the four have to do with each other

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

**The one ordering constraint that matters:** the INSIGHTS half of #1 wants
numbers that #2 produces — bytes, rows, durations and anything about the
infrastructure. #1's plan splits along that line so the ALERT half can ship
first and the INSIGHTS half lands after #2, rather than inventing a second
metrics pipeline that #2 would then replace.

**Split out of #3, and named so it is not rediscovered as a gap:** datasets and
data-aware scheduling — a step declaring it produces something, and a workflow
triggered when that something updates. It appeared in the target picture as the
`model_trained` node. It is a scheduling feature, not a graph one, and it gets
its own plan rather than riding along in #3.

#4 depends on nothing. It is last because it is least urgent, not because it is
blocked — the contract it consumes has shipped and is proven by a
three-language test.

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
