# What a step runs in

The graph draws a chip on each step saying its language and the tools it drives:

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.53.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

Most of the time you write nothing. The engine reads `run:` and `image:` and
works it out.

## When to declare it

Two fields, both optional, both per step:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: python job.py
    tools: [spark]         # the command says Python, and only you know it
                           # submits to a cluster
```

Declare when the inference is **blind or wrong**:

- a bare binary path — `/opt/app/start` could be anything;
- a wrapper script — `run.sh` that shells out to something else;
- an image whose name says nothing — `ghcr.io/acme/worker:3`;
- a tool the command does not name — a Python script that drives Spark.

What you declare **wins** over what the engine infers. You are allowed to know
better than the parser.

Declaring only `tools:` keeps the inferred runtime, which is the common case:
the command already said `python`, and you are adding what it drives.

## The vocabulary

An id outside these lists is **refused at publish**, naming what is valid. It is
refused rather than dropped because an id silently discarded renders as a
missing chip on a screen three days later, with nothing to trace it to.

| `runtime:` | |
|---|---|
| `python` `go` `node` `java` `rust` `php` `ruby` `dotnet` `sql` `shell` | |

| `tools:` | |
|---|---|
| `dbt` `spark` `airbyte` `soda` `sqlmesh` `meltano` `dlt` `duckdb` `pandas` `polars` `airflow` `terraform` | |

One runtime, any number of tools. They are separate because one step routinely
has both and ranking them has no right answer: `spark-submit job.py` is Spark
explaining the memory and Python explaining the stack trace.

Growing either list is a line in `internal/domain/runtimes` plus its test.

## What the engine works out on its own

It reads the command the way a shell would, so the shapes people actually write
all land:

| the command | reads as |
|---|---|
| `python fetch.py` | Python |
| `FOO=bar python x.py` | Python |
| `uv run python -m app` | Python |
| `cd /src && dbt build` | dbt |
| `python -m pip install -r req.txt && dbt build` | Python + dbt |
| `spark-submit --py-files a.zip job.py` | Python + Spark |
| `cp in.csv /tmp/ && python x.py` | Python — **not** Shell |
| `dbt build` | dbt — **not** Python |
| `dlt pipeline orders run` | dlt — **not** Python |
| `python -m dlt pipeline orders` | Python + dlt |
| `python pipelines/orders.py` | Python — **not** dlt |
| `./scripts/backfill.py` | Python |
| `/opt/brevis/bin/fetch-weather` | **nothing** |
| `{{ .cmd }}` | **nothing** |

Three of those rows are the interesting ones.

**A shell preamble does not hide the language behind it.** `shell` is the
weakest reading: anything concrete beats it, because the language whose stack
trace you are about to read is the useful answer.

**dbt is dbt, not Python.** It IS Python underneath, and nobody wants that on
the card.

**`python pipelines/orders.py` is not dlt**, even when the file is a dlt
pipeline from top to bottom. That is how most dlt steps are written, and it is
the shape the parser cannot read: nothing in the command names the library, and
a chip lit up from a *filename* would be a guess sitting next to the SDK badge,
which cannot lie and lends its credibility to whatever is drawn beside it.

Write it down instead — `tools: [dlt]` — and the chip renders solid, which says
you asserted it rather than the engine guessed:

```yaml
- name: load-orders
  run: python pipelines/orders.py
  tools: [dlt]
```

### The image is a second signal

Read from the reference's **last path segment only** — never the registry host,
because `ghcr.io/python-shop/anything` is not a Python image.

The command wins for the runtime; the image adds tools and supplies a runtime
only when the command supplied none. An image named `python:3.12` running
`dbt build` is a dbt step.

### When it says nothing, it draws nothing

No empty row, no reserved space, no chip reading "unknown". A card that says
nothing is honest; a card that says the wrong thing costs somebody the hour they
spend believing it.

## Where the chip's answer came from

The chip is drawn differently depending on its source, and the detail panel
spells it out:

| source | drawn | means |
|---|---|---|
| `declared` | solid border | you wrote it in the workflow |
| `inferred` | **dashed** border | the engine read the command or the image |

The distinction is the point. *"This step runs Python"* and *"this command
starts with `python`"* are different claims, and the badge beside it —
`SDK v0.53.0` — is trustworthy precisely because it is **observed**: the step
announces itself and nothing in the YAML can produce it. Anything drawn next to
that badge has to be honest about which kind of claim it is making.

An `observed` tier for runtimes, over the same `@brevis:` protocol the SDK badge
uses, is designed for and not built. See
[`docs/plan/2026-09-07-runtime-and-tooling-on-the-graph.md`](plan/2026-09-07-runtime-and-tooling-on-the-graph.md).

## What it deliberately does not do

**Versions.** `Python 3.12` would be more useful than `Python`, and neither the
command nor the image reliably carries one — a `:3.12-slim` tag sometimes does
and `:latest` never does. Guessing a version is worse than omitting one.

**Reading your source tree.** A `requirements.txt` or a `go.mod` next to the
workflow would be more accurate and would mean the engine reading the client's
repository, which it does not do and should not start.

## Where it is computed

At request time, on the definition the graph endpoint already loads — not stored
in a column.

The inference rules will be wrong at first. A stored value freezes a wrong guess
into every workflow published before the fix, and correcting it needs a
backfill. Computed on read, fixing the rule fixes history.
