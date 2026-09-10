# Parameters

> What changes between two runs of the same workflow, without editing the file.

*https://brevis.sh/en/docs/parameters/ · brevis.sh docs (en)*

---

A workflow can declare **run parameters**: values that change between two
triggers without the file changing.

## Declaring

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| field | | |
|---|---|---|
| `name` | **required** | the name used in the template and in `--param` |
| `type` | | `string` or `boolean` |
| `default` | | used when the trigger provides no value |
| `pattern` | | regular expression the value must match |

A parameter **without a `default`** is required: a trigger that omits it fails
before executing any step.

## Using it in the command

The value enters `run` through a template, in double braces with a dot:

```yaml
run: ./load.sh --since {{ .start_date }}
```

## Passing it on the command line

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` is repeatable. Input without `=` is an **error**:

```
erro: --param "load_full": use chave=valor
```

That is deliberate. `--param load_full`, forgetting the value, would run with
the default — and the operator would believe the backfill happened with the
parameter they intended.

## In a backfill

The values apply to **every** slot in the range. This is the central use case:
"reprocess all of January with `load_full=true`".

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## In the interface

A workflow with `params` gets a **form** in place of the simple trigger button.
Fields come from the declaration, and `pattern` is validated before submission.

## In the step environment

Run parameters also enter each step's **environment**, prefixed. This exists so
that a fetcher written with the [SDK](/en/docs/sdk/index.md) can see them without receiving
anything as an argument:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## Auto params

Every run also carries **auto params**: values the engine works out on its own.
Nobody declares them, every run has them, and they are on the run's screen.

The one that matters is `adjusted_at`, and the bug it removes is worth naming.
A fetcher reads `now()`, subtracts its window and asks the vendor for the last
two hours. On a run that starts on time that is right. On a run the queue
delayed by forty minutes it is forty minutes wrong — and those forty minutes
belong to **no run at all**, because the next slot reads its own `now()` too.
Nothing fails; the data is simply missing, and it is found weeks later.

So the engine hands over a clock instead. Read `adjusted_at` where you would
have read `now()`:

| param | | |
|---|---|---|
| `adjusted_at` | always | the clock this run should read: the slot when there is one, the start otherwise |
| `date` | always | `adjusted_at` as `YYYY-MM-DD`, UTC — the partition, the folder, the `WHERE` |
| `delay_seconds` | always | how late this attempt was against its slot; `0` on a manual run |
| `previous_error` | always | the run before this one did not succeed |
| `scheduled_at` | scheduled | the slot the cron asked for |
| `started_at` | | when this attempt began; a retry moves it |
| `interval_start` | scheduled | the **previous** slot |
| `interval_end` | scheduled | this slot, **excluded** |
| `previous_success_at` | | the slot of the last run that did succeed |

**Every timestamp here is UTC**, written as RFC 3339 with a `Z`, and the run's
screen shows them in UTC too — so the page and `$BREVIS_AUTO_ADJUSTED_AT` never
disagree. `date` is the **UTC day**, which for a workflow whose slot crosses
midnight UTC is not the local day: a `0 22 * * *` in `America/Sao_Paulo` has a
`date` of the following day. Airflow's `ds` behaves the same way, for the same
reason — the slot is an instant, and an instant has one day only once a
timezone is chosen.

`interval_start` and `interval_end` come from the **cron**, not from the
history: a backfill of a slot from March produces the window March had, not the
window this workflow's runs happen to describe today. A pipeline that asks for
`[start, end)` never overlaps and never gaps, however late it runs and however
often it retries.

They are a **snapshot**, computed when the run starts and stored on it.
`previous_error` is a fact about the instant this run began; recomputing it
tomorrow — after the previous run was retried and passed — would answer a
different question with the same name.

### Reading them

In a shell step, one variable per value, so nothing has to be parsed:

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

The whole set is also in `$BREVIS_AUTO_PARAMS` as one JSON object, for a step
that would rather parse once.

In Go, the [SDK](/en/docs/sdk/index.md) reads them for you:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` is the clock to read instead of `time.Now()`. Outside the engine —
a fetcher someone runs by hand — it *is* the wall clock, so nothing has to be
special-cased for local development.

In Python, `pip install brevis` reads the same values:

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`window()` returns `None` when the workflow has no schedule, rather than a pair
of zero times — a query from the epoch selects everything, and that failure
should not be silent. The `or` above is the whole fallback.

### In dlt, nothing to read at all

[dlt](https://dlthub.com) keeps its own cursor, in state it writes to the
destination. That state only moves **forward**, which is fine until you re-run
an old slot: the cursor still says today, so the backfill reads today's window,
and dlt's own answer for that is to drop the state.

The window Brevis hands over comes from the cron, so the March run produces
March's window however often it is retried. dlt will take it — the names are
its own, read before it goes looking for Airflow, and the engine sets them on
every scheduled run:

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` is all of it. `$DLT_INTERVAL_START` and
`$DLT_INTERVAL_END` become dlt's `initial_value` and `end_value`, half-open on
both sides — the row sitting exactly on the start is in, the one sitting exactly
on the end is not, which is what makes consecutive slots neither gap nor
overlap.

An incremental with an `end_value` **does not touch the persisted state**. That
is the part that matters: re-running one slot is idempotent, and a backfill of
March does not move the cursor that the nightly run depends on.

Two things to know before it works:

- **The cursor must be a datetime, not a string.** dlt will not coerce a `text`
  cursor for comparison and refuses to join the scheduler at all, with a
  `JoinSchedulerError` that does not obviously say so. Convert it on the
  resource — `add_map` — or type the `initial_value` as above.
- **A workflow with no schedule hands over nothing**, and dlt then raises
  `ExternalSchedulerNotAvailable` rather than quietly falling back to its
  cursor. If a pipeline should work both ways, leave
  `allow_external_schedulers` off and pass the window yourself from
  `run.window()`.

Declaring `tools: [dlt]` on the step is separate, and only draws the chip on the
graph — the window is handed over either way.

## The snapshot

Parameters are stored **in the run**, not read from the workflow at execution
time. The run carries the input it was triggered with, and the log of a January
execution keeps showing January's values even after the default changes.

## Next steps

- [Configuration](/en/docs/configuration/index.md) — process environment variables
- [SDK](/en/docs/sdk/index.md) — how a fetcher reads the run context
- [Context between steps](/en/docs/context/index.md) — what a step publishes for the next
