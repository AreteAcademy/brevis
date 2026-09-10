# docs

Everything here describes the **current state**. What was done is in the git
history; what is planned is in the [issues](https://github.com/AreteAcademy/brevis/issues).

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
| [`OBSERVABILITY.md`](OBSERVABILITY.md) | the metrics both processes emit, and what is deliberately absent |

## What is not here, and why

There are no dated specs and no phase reports. Until 2026-09-10 this directory
also carried 41 specs in `plan/`, seven phase reports in `phases/`, the engine's
original build prompt and five superseded SDK documents — about 750 KB recording
what had been decided on which day.

A document that tracks work goes stale in a week and then misleads, and the git
history already records what shipped, commit by commit. So the rule is the one
at the top: a document here describes how the system behaves now, or it does not
belong here.

That material is not lost. It is in the history, and the tag
`archive/docs-2026-09-10` is the readable name for the commit that still has it:

```bash
git show archive/docs-2026-09-10:docs/plan.md
git ls-tree --name-only archive/docs-2026-09-10 docs/plan/
```

Anything from those specs that a reader of the code would need was moved into
the document or the comment that needed it — the OTel package measurement into
`OBSERVABILITY.md`, the load-trend measurement into `stages.go`, the
default-configures-nothing rule into `CONTRIBUTING.md`.
