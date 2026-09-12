# Go, Polars, Go

Three steps, two languages, one run. Go reads the CSVs and lands the result;
Polars does the group-by in the middle.

```
extract (Go)  ──▶  shape (Polars)  ──▶  load (Go)
 2,000 orders      120 clients          Postgres
```

## Why the middle step exists at all

`sdk.Transform` is `func(payload any) (any, error)` — **one record at a time**.
That is the right shape for renaming a field, computing a key, or dropping a
column, and it cannot express *"revenue per client across the whole file"*: a
group-by needs to see every row before it can emit the first one.

So the aggregation goes where aggregations are good, and the SDK keeps the parts
it is good at — the source, retries, provenance, the dedup key, the transaction.
Neither side does the other's job.

## The handoff

The records do not travel through the context. **A path does.**

```go
bctx.Set("path", res.Objects[0])          // extract, in Go
```
```python
staged = context.get("extract.path")      # shape, in Python
```

The context is capped at 4 KB, which is the constraint that makes this the only
sane design: two thousand rows have no business in an environment variable, and
a bounded context is what keeps a fan-out bounded.

Note `res.Objects[0]` — the path is read back from the **result**, not rebuilt
from the string passed in. `to.Files` chooses the filename, timestamp included;
guessing it would work right up until it did not.

## Running it

Postgres, Polars, and the context library. From the repository root:

```sh
docker compose -f docker-compose.drivers.yml up -d postgres

python3 -m venv .venv && . .venv/bin/activate
pip install polars lib/python-context

cd examples && ./14-polars/run-local.sh
```

`run-local.sh` is the engine's own protocol done by hand: each step gets a
`BREVIS_OUTPUT` to publish into, and the next gets a `BREVIS_INPUT` holding what
the earlier steps published, keyed by step id. Under the engine, `workflow.yaml`
does this and nothing in the three steps changes.

**Run it twice.** The second load reports `120 ignored`.

## Two things worth reading the code for

**Every CSV column arrives as a string, and the cast lives in `shape.py`.** The
CSV reader keeps what the file said instead of inferring types — *"a CSV has no
types, and inventing them here would guess."* So the schema is declared once, in
the step that has a dataframe, and a bad value fails loudly there.

**An aggregate has no timestamp of its own.** It is a snapshot, so the only
honest date is the one the *run* is for, and the engine supplies it as
`BREVIS_AUTO_DATE`. That date goes into the `ingestion_id`, which is what makes
re-running a slot replace its rows instead of appending a second truth:

| | rows written |
|---|---|
| first load | 120 |
| same run date, again | 0 — all 120 ignored |
| a different run date | 120 — a new snapshot |

Taking `time.Now()` there would make every retry a new row. That is the failure
that looks like success: the table grows and every number in it is correct.

## What this is not

It is not the `sdk.Frame(sdk.Polars, …)` stage — that is a separate design, and
this is the shape that works with what exists today. The trade is real and worth
knowing: one file on disk between steps, in exchange for Go never holding more
than one record at a time. Only the Python step materialises a frame.

## The data

`../testdata/shop/` — 120 clients, 350 products, 2,000 orders, deliberately
uneven. Twelve clients place a third of the orders, some carry a discount, and a
quarter of the file is `cancelled` or `refunded`. Revenue means `paid +
shipped`, so forgetting that filter is visibly wrong rather than plausibly
wrong — R$ 2,625,941.39 across 1,508 orders is the right answer.
