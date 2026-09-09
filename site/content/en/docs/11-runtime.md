---
title: Step runtime
description: What a step declares about the language and tools it drives — and why you rarely need to.
group: Reference
order: 11
slug: runtime
---

The graph draws a chip on each step saying its language and the tools it drives:

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**Most of the time you write nothing.** The engine reads `run:` and `image:` and
works it out.

## When to declare it

Two fields, both optional, both per step:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| field | |
|---|---|
| `runtime` | the step's language: `go`, `python`, `node`, `shell`… |
| `tools` | the tools the command drives: `dbt`, `dlt`, `spark`… |

The rule of thumb: **declare it when inference gets it wrong.** A `run:` that
points at a compiled binary has no way of saying it is Go; a wrapper that calls
`dbt` internally has no way of saying it drives dbt.

## Why a closed vocabulary

An id outside the vocabulary is **refused at publish**, naming what is valid.
The alternative is a chip that renders blank on a screen three days later, with
nothing to trace it to.

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## What the chip is for

It is not decoration. When a run fails at 3 a.m., the first question is "what
does this step execute?", and the answer usually lives in another repository.
The chip puts it on the graph itself — alongside the SDK version the step used,
which is what tells "broke today" apart from "broke since Tuesday's bump".

## Next steps

- [Workflows](/docs/workflows/) — every step field
- [Pod per step](/docs/pod-per-step/) — where a step actually runs
- [Observability](/docs/observability/) — the metrics that go with it
