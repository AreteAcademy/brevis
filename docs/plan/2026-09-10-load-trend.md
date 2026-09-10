# The load trend screen

**Written on** 2026-09-10 · **Base** engine `v0.12.0`, `sdk/v0.58.0`
**Status** accepted — TASK.md #9

> *"boa parte do sistema de ingestao tem problemas de carregamento,
> dimencionamento dos dados, datasets grandes, carregamento lento"*
> — [`plan/2026-09-08-backlog.md`](2026-09-08-backlog.md) §11

The backlog split that note in two and put (a) first: the numbers already exist,
so keep them over time and draw the trend. This is that, with one correction the
backlog could not have known.

---

## 1. What the backlog got right, and the one thing it did not

Right, and worth restating: **no new measurement**. Every number this screen
draws is already produced, already crosses the `@brevis:` pipe, and is already
stored — `task_runs.etapas`, one JSONB array per attempt, written by the same
collector that draws the phase boxes on a run's page.

So the backlog called it *"a query, a screen and a retention policy"*.

It is a query, a screen, a retention policy **and a table**, and the difference
was measured rather than argued.

### The measurement

A probe database seeded with a year of hourly runs — 350,000 runs across 40
workflows, one SDK step each, `etapas` shaped exactly as the collector writes
it. The question is the one the screen asks: *ninety days of one workflow,
grouped by day*, with every aggregate the screen draws. Warm cache, median of
three readings.

| the shape | buffers read | time |
|---|---|---|
| `jsonb_array_elements` over `task_runs` | 14,913 | 22 ms |
| a narrow derived table, indexed | **2,243** | **5 ms** |

**Six and a half times the reads, four times the latency**, for one panel on one
page.

The first figures written here were worse than that and they were wrong: they
compared a four-column select against a nine-column one, which flatters whichever
side you happen to be arguing for. Re-measured with both sides asking the same
question, the gap is smaller and still decisive.

Both rows above give the JSONB version its **best** case — an index on
`runs (workflow_slug, criado_em)` that this schema does not have. Adding it buys
around 20%, because the cost is not the scan over `runs`. It is `task_runs`
being a **wide** row: `etapas` sits beside `log` and `saida`, and the trend has
to touch all of it to read nine numbers. 222 MB of table to answer a question
whose answer is 81 MB.

That is the whole argument for a table. It is not premature: the alternative was
built and measured first.

### What it does NOT justify

A metrics subsystem, a time-series database, or a second process. One narrow
table, written where the stages are already recorded, read by one query. The
premise stands — the binary links nothing new.

---

## 2. The table

```
load_metrics(run_id, node_id, map_index, workflow_slug, em,
             linhas, registros, ignorados, bytes_saida, load_ms,
             bytes_entrada, paginas, tentativas, extract_ms)
```

No `status` column, and that is the decision rather than an omission. A load
that died halfway measured half of something that never happened; averaged into
a trend it reads as a dataset shrinking, and the run's failure is already on the
calendar. Only a load phase that reached `done` is kept, so there is nothing for
the column to say.

Denormalised on purpose. `workflow_slug` and `em` are already on `runs`, and
joining to get them is what made the JSONB version slow — the index that makes
this table answer in one scan has to live on the table being scanned.

**Written once, when the step ends**, from the same collector the phase boxes
come from — and NOT in `markStages`, which runs on every marked line because the
screen has to advance while the step is alive. A row rewritten per phase
transition would be a database round trip each time, to store a number that is
not final yet.

**Upserted, not inserted**: attempt 2 overwrites attempt 1. A step that failed
after loading 40,000 rows and then succeeded loading 48,000 loaded 48,000, and
summing both would invent 88,000 that never existed. `task_runs` keeps the
per-attempt history for whoever needs it.

A step that reports no load writes no row: a fetcher that is not an SDK pipeline
has nothing to contribute to a load trend.

**Backfilled by the migration** from `task_runs.etapas`. The history exists; a
trend screen that starts empty on an installation with a year of runs would be
the screen lying about a fleet it can already see.

It costs **10.8 seconds for 350,000 task_runs** holding 196 MB, measured, inside
the migration transaction — so that is ten seconds added to one upgrade. Not
deferred to a background job, which would buy those ten seconds at the price of
a screen that is wrong for an hour after every upgrade and a second code path
that exists forever.

---

## 3. Two numbers the pipe does not carry yet

`loadNumbers` in `sdk/telemetry.go` sends `rows` and `records`. `Result` also
has `Bytes` (what the load actually wrote) and `Ignored` (rows deduplication
matched as already present), and neither crosses.

`bytes_out` is not optional here. *"Which pipeline's bytes are growing faster
than its rows"* is one of the note's own questions, and it cannot be asked from
the extract's byte count alone: a load that doubles in size while its row count
holds flat is a schema that grew a column, which is exactly the diagnosis this
screen is for.

Both go in `loadNumbers`, skipped at zero like every other number there.

---

## 4. The screen

On the workflow page, under the calendar. The calendar answers *did it run*; the
trend answers *is it getting worse*, and they are the same question at two
resolutions.

Four series, because four is what the note asked for:

| series | the question |
|---|---|
| rows per run | is the dataset growing |
| bytes per row | is each row getting fatter — a schema that grew |
| extract seconds | is the source getting slower |
| load seconds | is the destination getting slower |

Drawn from the same primitives as the calendar heatmap: server-rendered SVG, no
library. A chart that needs a runtime is a chart that does not render on the
page somebody opens during an incident.

---

## 5. Retention, and why it is not in this change

The table grows at roughly **81 MB per 350,000 rows** — a year of hourly runs
across forty workflows, measured on the probe. That is small enough that shipping without a retention
policy is honest rather than negligent, and large enough that "forever" is not
an answer either.

It is left out because **it deletes the owner's data**, and where the line falls
— ninety days, a year, per workflow, never — is not a default worth inventing.
Nothing else in this schema deletes anything today: `runs` and `task_runs` have
grown without bound since the first migration, and a retention policy that
covered only the newest table would be an odd place to start.

Raised for a decision rather than guessed at. When it is answered it is a
`DELETE` on one indexed column, and no schema change.
