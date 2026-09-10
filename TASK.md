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
| **6** | **Schema evolution on load** — `CreateTable` on every SQL destination, then additive evolution | [`plan/2026-09-08-schema-evolution.md`](docs/plan/2026-09-08-schema-evolution.md) | **done**, except Redshift |
| **7** | **dlt in the vocabulary** — a constant, a marker, a test. The chip lights up when it is declared and when the command names it | [`plan/2026-09-09-integracao-nativa-com-dlt.md`](docs/plan/2026-09-09-integracao-nativa-com-dlt.md) §2 | **done** |
| **8** | **The run window as dlt's cursor** — `run.window()` bridged to its incremental loading, so a backfill reads the slot it is for | [`plan/2026-09-09-integracao-nativa-com-dlt.md`](docs/plan/2026-09-09-integracao-nativa-com-dlt.md) §3 | **done** — two env vars, no library |
| **9** | **The load trend screen** — the numbers every load already produces, kept and drawn | [`plan/2026-09-10-load-trend.md`](docs/plan/2026-09-10-load-trend.md) | **done** — retention is the owner's call |
| **10** | **A third executor** — a host the engine does not own. dlt is its first consumer, Cloud Run and Lambda the next | [`…-com-dlt.md`](docs/plan/2026-09-09-integracao-nativa-com-dlt.md) §4 + [`backlog`](docs/plan/2026-09-08-backlog.md) §8 | **next** — **decide the return path first** |
| **11** | **SQLite, and maybe MySQL** — a default that needs no container; the queue's guarantee re-proved per backend | [`plan/2026-09-09-backlog-12-sqlite-mysql.md`](docs/plan/2026-09-09-backlog-12-sqlite-mysql.md) | proposed |
| **12** | **Node.js context library** — the Python contract, in npm | [`plan/2026-09-08-node-context-sdk.md`](docs/plan/2026-09-08-node-context-sdk.md) | proposed |
| **13** | **AI as a transform** — one OpenAI-compatible client, batched, cached, and refusing output it did not ask for | [`plan/2026-09-09-ai-como-transform.md`](docs/plan/2026-09-09-ai-como-transform.md) | proposed |
| **14** | **DuckDB and MotherDuck** — `from`/`to` in a module of their own, because the driver needs cgo | [`plan/2026-09-09-duckdb-e-motherduck.md`](docs/plan/2026-09-09-duckdb-e-motherduck.md) | proposed |
| **15** | **Official task images** — `etl-go`, `etl-python`, `etl-node`, with a size gate | [`plan/2026-09-08-backlog.md`](docs/plan/2026-09-08-backlog.md) §7 | proposed — deferred by the owner |
| — | **Step metrics** — `brevis.metrics` and `sdk.StdoutMeter` on the engine's `/metrics` | [`plan/2026-09-09-step-metrics.md`](docs/plan/2026-09-09-step-metrics.md) | **done** — `v0.12.0`, `sdk/v0.58.0`, py `0.3.2` |

The audits that produced this order:
[`plan/2026-09-08-open-threads.md`](docs/plan/2026-09-08-open-threads.md) — what
is open; [`plan/2026-09-08-backlog.md`](docs/plan/2026-09-08-backlog.md) —
the owner's raw notes read against the tree; and
[`plan/2026-09-09-integracao-nativa-com-dlt.md`](docs/plan/2026-09-09-integracao-nativa-com-dlt.md)
§8 — the first consumer's proposal reviewed against it.

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

**#6 shipped.** Postgres and MySQL create their tables from a declared Schema
and evolve them additively; BigQuery already created. Redshift does neither, and
that is written down rather than hidden: there is no Redshift in CI, and a
rendered-DDL test would be a checkmark that means less than it looks.

## The dlt proposal, and the entry it merged with

The first consumer's proposal was reviewed against the tree and accepted. It is
three asks of very different size, and splitting them across #7, #8 and #10 is
deliberate: the first is hours and the third is architecture, and bundling them
would hold the cheap one hostage to the expensive one.

**Its third ask and the old "beyond Kubernetes" entry are the same executor**,
and they were written independently. Both derive the same agenda from the same
interface: a target the engine does not own has no equivalent of
`terminationMessagePath`, of `follow=true` logs, or of deleting a pod to cancel.
Two people reaching that list separately is the strongest evidence it is the
right list — so they are now one entry, and the first deliverable is still a
decision rather than code.

The thesis is worth restating because it orders the rest: **Brevis does not
exclude, it integrates.** dlt has extract and load solved and scheduling
unsolved; Airbyte brings its own scheduler and would compete. While this SDK is
still young, a user who needs a connector Brevis does not have should reach for
dlt *inside* Brevis, not for Airflow outside it.

**#11 is bigger than it reads.** "Add SQLite and MySQL" is not a driver: the
queue's whole correctness rests on `FOR UPDATE SKIP LOCKED`, which SQLite does
not have at all and MySQL has with different semantics. A second backend is a
second PROOF that no run is handed out twice, and the plan splits SQLite (a
default that needs no container, single writer, honest about it) from MySQL (the
expensive half, worth building when a customer asks).

**#15 was deferred by the owner**, on 2026-09-09.

## The three notes added on 2026-09-09

**§13, AI as a transform, is the one with a real strategic argument** — *"sem ele
teremos um atraso em relação ao mercado"* — and it is also the one with the most
ways to be wrong quietly.

The seam already exists: `Transformer` is `func(any) (any, error)` and an HTTP
call to a model fits it today, with no SDK change. So the question is what the
SDK should OWN, and the answer is what a hand-written call gets wrong: batching,
a cache keyed by input, a declared set of allowed answers, and 429 as a retry
rather than a failure.

What the plan insists on is that **an AI call breaks two invariants the rest of
the SDK rests on**. Every other transform is free and deterministic; this one
costs money per record and returns something different the second time. A
retried load of 50,000 records re-calls the model 50,000 times, and a rerun
produces different data for the same input — which is what `ingestion_id`,
checkpoints and safe retries all assume does not happen. Neither is a reason not
to build it; both are reasons to name them in the design instead of in an
invoice.

And one client, not four. OpenRouter *is* an OpenAI-compatible endpoint fronting
the others, so the vendor is a URL and a model string. Four clients buy four
things to keep working.

**§14 and §15 are the same work, and one measurement decides both.** The DuckDB
driver does not compile with `CGO_ENABLED=0` — measured, not assumed — and
drags 135 packages. The Dockerfile, `engine-weight.sh` and `pruning-check.sh`
all build with cgo off *because that is what ships*, so a cgo dependency in
`sdk/` would make every consumer, including the one that reads a CSV, need a C
toolchain. It goes in a module of its own, like `otelmeter`, with a pruning case
proving the main SDK did not grow.

MotherDuck is the same driver with `md:` instead of a file path, so the answer
to *"temos capacidade de rodar Brevis + MotherDuck"* is **yes, and it costs
exactly what the DuckDB connector costs** — it is the same code.

Worth saying plainly: **DuckDB already works under Brevis today** as a step,
`run: duckdb …`, and it already draws its own chip. The ask is the SDK
connector, which is the expensive half of a thing that partly exists.

## Standing rules

From `CONTRIBUTING.md`, restated because this list is where they get tested:

- **The binary stays light.** Stated by the owner on 2026-09-09 as a premise for
  every item here, and it is already the constraint that decided three of them:
  the OTLP exporter was refused (49 packages, 29 protobuf), step metrics went
  through the pipe that already existed instead of a Pushgateway, and the DuckDB
  driver goes in a module of its own because it needs cgo.

  It is enforced and not merely intended. `engine-weight.sh` caps what
  `./cmd/brevis` links and names the modules that may never appear;
  `pruning-check.sh` caps each SDK consumer separately, so a fetcher that reads
  a CSV does not pay for BigQuery. **Weight that only one consumer wants goes in
  a module of its own** — `sdk/metrics/otelmeter` is the pattern — and the
  pruning gate is what proves the main module did not grow.

  A ceiling that moves is a ceiling that is not one: if a number here has to
  rise, the commit that raises it says what was bought.
- **A test that would fail without the change**, and proof it bites — revert the
  change and watch the test go red.
- **A feature with a default gets a test that configures nothing.** Two features
  shipped switched off in one week, and both times every test set the field
  under test. See [`plan/2026-09-07-open-threads.md`](docs/plan/2026-09-07-open-threads.md).
- **A number that is always zero is worse than no number**, and a badge that can
  lie is worse than no badge.
- **English**, everywhere except the site's user-facing content.
