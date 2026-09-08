# A complete workflow, in Go

Every shape the engine has, in one pipeline, with every step written against the
SDK. It reads three CSVs and writes a summary — the pipeline is not the point.
The point is that each step is here because it demonstrates something the graph
can do.

```bash
make up      # builds the engine from this tree, brings the stack up
make run     # queues a run right now
make down    # stops everything
```

Then open **http://localhost:8080** and read the graph beside the file.

## What it draws

```
start ─▶ discover ─▶ load [3] ─▶ check ─▶ report ─▶ quality.count_rows ─▶ …
  │        └──────── ingestion ────┘      └──── reporting ────┘
  │
  └─▶ notify_failure (any_failed)        cleanup (all_done) ─▶ end
```

| step | what it is here for |
|---|---|
| `start`, `end` | `marker: true` — a step with no command, no process, no pod |
| `discover` | publishes the list the next step maps over |
| `load` | **`for_each:`** — one node, three instances, `[3]` on the card |
| `check` | exists *because* a mapped step publishes nothing downstream |
| `report` | **`unless_empty:`** — a key, not an expression; and the only step that needs a clock, which it gets from the auto params |
| `quality.*` | **`uses:`** — another workflow's steps, expanded at publish |
| `notify_failure` | **`when: any_failed`** |
| `cleanup` | **`when: all_done`** — impossible without trigger rules |
| the arrows | two carry **labels**; the rest do not, which is the normal case |
| the boxes | **`group:`**, collapsible |

## Watching each branch

```bash
make run       # the happy path: 3 partitions, 8 rows, 4 SKUs in the report
make empty     # one partition with NO rows -> `report` is skipped on unless_empty
make nothing   # no partitions at all      -> `load` is skipped on an empty list
make refill    # back to the three real partitions
```

`data/partitions` is the committed input and never moves; `data/incoming` is
what the pipeline reads, and those targets rebuild it. `make up` refills it, so
a fresh clone runs on the first try.

`make empty` and `make nothing` are worth running. The graph fills with
**`skipped`** — a state of its own, in its own colour, that is neither a failure
nor a success — and every skipped step says which step stopped it:

```
load    | skipped: `discover.partitions` is an empty list
check   | skipped: `load` was skipped
report  | skipped: `check.has_rows` is empty
```

Note that a run of `daily_sales` is **idempotent per logical date**: `make run`
twice on the same day queues nothing the second time. Use a different date to
force another:

```bash
docker compose run --rm --no-deps api backfill daily_sales --from 2026-09-01 --to 2026-09-01
```

## The Go side

One module, one binary, several subcommands — which is what a real deployment
looks like: one image, many entrypoints. `pipeline/cmd/pipeline` has them all.

Two shapes of the SDK appear, on purpose:

- `load` and `report` use **`sdk.Run(sdk.Pipeline{…})`**, so the phases each
  pipeline goes through appear on the screen *while it runs*. A forty-minute
  step that only says "running" is a step nobody can help.
- `discover`, `check` and the quality steps are plain Go using
  **`sdk/context`**, which costs `encoding/json`, `os` and `strings` and nothing
  else.

The context is how the steps talk. There is no API call and no token: the engine
hands a step `BREVIS_INPUT` when it starts it and reads `BREVIS_OUTPUT` back
when it ends.

```go
// discover
bctx.SetAll(map[string]any{"partitions": partitions})

// load, one instance per element
partition := os.Getenv("BREVIS_MAP_VALUE")
```

## The clock the report reads

`report` names its output after `sdk.RunContextFromEnv().Auto.Now()` and never calls
`time.Now()` — `data/reports/2026-09-08/`. Nothing in the YAML sets that up:
every run carries the [auto params](https://brevis.dev/docs/parameters/#auto-params),
and `Auto.Now()` is the **slot**, so a run the queue delayed past midnight does
not write yesterday's data into today's folder, and a retry at nine the next
morning does not write it into a third one.

It also prints the window it covers, which comes from the cron rather than from
the history:

```
reporting on the window [2026-09-07T04:00:00Z, 2026-09-08T04:00:00Z)
```

Run the binary by hand and `Auto.Now()` is simply the wall clock, which is why
there is no branch in that function for local development.

## Two traps this example steps around

**A destructive `when: all_done` cleanup beside a run-level retry.** A retry
re-runs the steps that failed — so a cleanup that deleted *this run's* staging
would delete exactly what the retry of `report` needs, and the run would fail
forever on "no such file or directory". `cleanup` here removes only what
*previous* runs staged. It is worth reading the comment on that function.

**A mapped step cannot publish context.** Three instances under one step's name
are three values for one key, and there is no honest answer to
`context.String("load.bucket")`. That is why `check` exists as a step of its
own, and the step says so in its own log rather than dropping the value quietly.

## It builds the engine from this tree

`docker-compose.yml` has no image tag: `when:`, `unless_empty:`, `for_each:`,
`group:`, `uses:` and markers are all newer than any published image, and
pinning a tag would make the first `make up` fail on "`when` is not valid" —
which reads as the example being wrong.

It also makes this a gate. If the engine stops running this, the example stops
coming up.

## Where the numbers are

```
http://localhost:9090/metrics   the API
http://localhost:9091/metrics   the scheduler, which is where the work happens
```

`brevis_queue_depth`, `brevis_claim_latency_seconds`, `brevis_step_attempts_total`
and the rest. On a port of its own, never the one the UI is on.

## Alerts

The alert pod only comes up with a webhook, because a delivery process with
nowhere to deliver drains the outbox into "undelivered" as fast as it fills:

```bash
make up WEBHOOK=https://hooks.slack.com/services/...
```

`load` declares `on_error: {type: SLACK}`, so it announces its own failures
beside the run-level alert. Where the message goes is the installation's
decision, never the workflow file's.
