# docs

**Updated on** 2026-09-08

> What is being worked on right now, and in what order: [`TASK.md`](../TASK.md).

---

## SDK

Start here. These four describe the SDK as it is today, and each answers a
different question:

| document | answers |
|---|---|
| [`SDK_ARCHITECTURE.md`](SDK_ARCHITECTURE.md) | **what and where** — a fetcher's four questions, the package map, the two interfaces, the path a record takes |
| [`SDK_NEW_DRIVER.md`](SDK_NEW_DRIVER.md) | **how** — the walkthrough for adding a driver, with the eight rules and the checklist |
| [`SDK_MATRIX.md`](SDK_MATRIX.md) | **what supports what** — every option per driver, the refused combinations, and what is not true yet |
| [`SDK_DECISIONS.md`](SDK_DECISIONS.md) | **why** — each decision, what was tried before it, and what that cost |

Each carries a `Valid for` stamp naming the version it was checked against.
When the stamp is behind the current tag, the document is a claim nobody has
re-verified — read it that way.

The API reference is the [godoc](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk)
and [`sdk/README.md`](../sdk/README.md); the version-by-version history is
[`CHANGELOG.md`](../CHANGELOG.md).

## Operations

| | |
|---|---|
| [`PUBLISHING.md`](PUBLISHING.md) | how to publish the SDK and the images |
| [`IMAGES.md`](IMAGES.md) | the Docker images |
| [`KUBERNETES.md`](KUBERNETES.md) | the deployment |
| [`PARAMS.md`](PARAMS.md) | the engine's run parameters |
| [`BRAND.md`](BRAND.md) | the mark, the palette and the type — what they are and why |
| [`RUNTIME.md`](RUNTIME.md) | what a step runs in, on the graph: `runtime:` and `tools:` |
| [`CONTEXT.md`](CONTEXT.md) | what one step tells the next, in any language |
| [`COMMANDS.md`](COMMANDS.md) | the command-line reference |

## Plans and specs

`plan/` holds the specs, in chronological order. Each was written before the
change and executed after it, so it serves as a record of what was asked for and
what actually shipped — **not** as a description of the current state.

| spec | became |
|---|---|
| [`2026-09-03-sdk-recebe-contexto-do-engine.md`](plan/2026-09-03-sdk-recebe-contexto-do-engine.md) | `v0.10.0` |
| [`2026-09-03-sdk-conserto-do-merge.md`](plan/2026-09-03-sdk-conserto-do-merge.md) | `v0.12.0` |
| [`2026-09-03-sdk-schema-declarado.md`](plan/2026-09-03-sdk-schema-declarado.md) | I1 = `v0.18.0`, I5 = `v0.24.0`, I2/I3/I4 = `v0.35.0` — see §14 of `SDK_DECISIONS.md` |
| [`2026-09-03-sdk-validacao-do-consumidor.md`](plan/2026-09-03-sdk-validacao-do-consumidor.md) | `v0.17.0` |
| [`2026-09-04-sdk-uma-declaracao-de-colunas.md`](plan/2026-09-04-sdk-uma-declaracao-de-colunas.md) | `v0.18.0` |
| [`2026-09-04-sdk-drivers-mvp.md`](plan/2026-09-04-sdk-drivers-mvp.md) | phase 0 = `v0.19.0`, phase 1 = `v0.20.0`; **phases 2–5 open** |
| [`2026-09-04-sdk-metadado-vira-transformer.md`](plan/2026-09-04-sdk-metadado-vira-transformer.md) | `v0.24.0` |
| [`2026-09-04-sdk-http-autenticacao.md`](plan/2026-09-04-sdk-http-autenticacao.md) | §3.2/3.3/3.4 = `v0.26.0`, §3.1 = `v0.27.0`; the deviations are in §6 of the spec itself |
| [`2026-09-05-contexto-entre-passos.md`](plan/2026-09-05-contexto-entre-passos.md) | **proposal** — what one step tells the next |
| [`2026-09-06-open-threads.md`](plan/2026-09-06-open-threads.md) | **inventory** — what was left open, checked against the tree, and in what order to close it |
| [`2026-09-06-english-only.md`](plan/2026-09-06-english-only.md) | threads A–J closed; what is left needs a cluster or credentials |
| [`2026-09-07-runtime-and-tooling-on-the-graph.md`](plan/2026-09-07-runtime-and-tooling-on-the-graph.md) | **done** — the runtime and tool chips on the DAG |
| [`2026-09-07-python-context-sdk.md`](plan/2026-09-07-python-context-sdk.md) | **done** — a Python package for context, and the contract under it |
| [`2026-09-07-open-threads.md`](plan/2026-09-07-open-threads.md) | **inventory** — what is still open, checked against the tree |
| [`2026-09-07-proofs-that-need-infrastructure.md`](plan/2026-09-07-proofs-that-need-infrastructure.md) | **proposed** — three paths CI does not exercise, and what each needs |
| [`2026-09-07-the-website.md`](plan/2026-09-07-the-website.md) | **proposed** — the site's source, its keys, and the Spanish it promises |
| [`2026-09-07-deferred-by-design.md`](plan/2026-09-07-deferred-by-design.md) | **reference** — what was decided against, and what would change each decision |
| [`2026-09-08-alerts-and-reports.md`](plan/2026-09-08-alerts-and-reports.md) | **proposed** — `TASK.md` #1: an alert pod, per-step alerting, a scheduled report |
| [`2026-09-08-observability.md`](plan/2026-09-08-observability.md) | **proposed** — `TASK.md` #2: OpenTelemetry metrics, and the ceiling they run into |
| [`2026-09-08-flow-shapes.md`](plan/2026-09-08-flow-shapes.md) | **proposed** — `TASK.md` #3: conditionals, trigger rules, sub-flows |
| [`2026-09-08-node-context-sdk.md`](plan/2026-09-08-node-context-sdk.md) | **proposed** — `TASK.md` #4: the context contract, in npm |

The specs are written in Portuguese where they were written that way, and they
stay: a spec records a decision made on a date, and rewriting a record is not
translating a project. New ones are in English.

`phases/` belongs to the **engine**, not the SDK: the orchestrator's build
phases.

## History — these are not the current state

These describe versions that no longer exist. They stay for the record; nothing
here should be read as today's API.

| | describes |
|---|---|
| [`SDK.md`](SDK.md) | the original build prompt for the SDK, from 2026-09-02 |
| [`SDK_V2.md`](SDK_V2.md) | the evolution asked for in `v0.2` |
| [`SDK_LOAD.md`](SDK_LOAD.md) | the load fix in `v0.2.1` |
| [`SDK_V9.md`](SDK_V9.md) | the consumer's report on `v0.9.x` |
| [`SDK_CONSUMIDOR.md`](SDK_CONSUMIDOR.md) | what the first consumer found between 2026-09-02 and 2026-09-04, and what changed because of it |
| [`plan.md`](plan.md) | the engine's original build prompt |
| [`gaps-yaml-vs-plano.md`](gaps-yaml-vs-plano.md) | a survey from August |
