---
title: Client libraries
description: How a step in any language reads the context and the run's clock — and which languages already have a library.
group: SDK and libraries
order: 13
slug: libraries
---

A step needs two things from the orchestrator: **what the previous step
published** and **which clock this run should read**. The client libraries hand
over those two, and nothing else.

## The contract comes before the libraries

What the engine defines is **two environment variables and a file**:

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — what this step's dependencies published |
| `BREVIS_OUTPUT` | a path to write **one** JSON object to |

Plus the run's variables and the auto parameters, all read-only: `BREVIS_RUN_*`
and `BREVIS_AUTO_*`. Under Kubernetes `BREVIS_OUTPUT` is
`/dev/termination-log`, which the engine already reads to get the exit code.

So there is **no API call, no token and no port**. A step in any language takes
part with a JSON parse and a file write:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

A library here opens no new capability — it saves you the validation, the
absent-value handling and the error messages that are worth not rewriting.

## Which ones exist

| language | | |
|---|---|---|
| **Python** | `pip install brevis` | [available](/docs/python/) — `0.2.1` on PyPI |
| **Go** | `brevis/sdk/context` | available, inside the SDK module |
| **Node.js** | — | planned |
| **Rust** | — | planned |
| anything else | the variables above | today |

:::note Go does not live in `lib/`
Go's context support lives in the `sdk/` module the project already publishes,
as a subpackage that prunes to nothing for anyone not importing it. A
`lib/go-context` would mean a second Go module for a package that has one.

The rule is **one directory per language that does not already have a published
artifact**.
:::

Each library is a separate artifact with its own version and tag: a fix in the
Python client does not force an engine release.

## What they are, and what they are not

They are **thin clients over the contract**, not ports of the Go SDK.

A port of the SDK's ETL machinery — drivers, pagination, checkpoints, ingestion
ids — does not belong here and probably nowhere: every language this would
target already has better tools for that, and Brevis's job is to run them. Which
is why the Python library exists so that a team can use pandas, Polars or dbt
and still get the one thing an orchestrator is for.

## Metrics

The library **emits** metrics, and the engine is what exposes them:

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

They come out of the **engine's** `/metrics`, on the scheduler's port, labelled
with the workflow and the step — see [Observability](/docs/observability/).

**This does not open a port, and it could not.** A step runs in its own pod for
forty seconds and exits; a port it opened would be scraped never, or once by
luck. So the line goes to stdout — the same pipe the engine already reads to
draw a pipeline's phases — and the engine records it. Nothing to install,
nothing to operate, and the labels are the engine's because a step cannot know
its own workflow slug.

The API details are in [Python](/docs/python/#metrics-only-your-pipeline-knows).

## Writing a client for another language

If you get to Node.js or Rust before we do, here is what a client has to get
right — and what the Python implementation already learned the hard way:

1. **Keys qualified by step.** `get("bucket")` with no step must be refused. Two
   steps may publish `bucket`, and picking one by search is precedence by
   accident.
2. **Merge the writes.** Two calls with different keys keep both; the same key
   twice keeps the last.
3. **Flush only on process exit.** A step that dies hard publishes nothing —
   which is correct, because a crashed step's output described work that did not
   finish.
4. **Refuse above 4096 bytes.** The platform **truncates** rather than refuses,
   and an object cut in half arrives as invalid JSON for the next step.
5. **Work outside Brevis.** With no `BREVIS_INPUT`, `get()` returns the default
   and `set()` accepts and discards, without raising: a script that cannot be
   run by hand cannot be developed.

## Next steps

- [Python](/docs/python/) — the complete API
- [Context between steps](/docs/context/) — the concept and the 4096-byte ceiling
- [Parameters](/docs/parameters/) — the declared ones and the automatic ones
