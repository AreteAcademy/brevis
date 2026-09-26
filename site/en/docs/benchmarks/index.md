# Benchmarks

> How we measure the gateway, what the numbers say, and the three witnesses that have to agree.

*https://brevis.sh/en/docs/benchmarks/ · brevis.sh docs (en)*

---

One command, and it measures the whole gateway:

```bash
BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
```

It brings up a Postgres, starts the gateway, loads it with [k6], **waits for
the buffer to drain**, scrapes the gateway's own `/metrics` and writes
`bench/results/REPORT.md`.

## Three witnesses, and the third is the one that matters

k6 knows what a producer saw. The gateway knows what it did. A load test
carrying only the first cannot tell a fast gateway from one that answers `202`
and drops what it accepted.

So the report puts the two side by side — and counts the rows in Postgres:

| witness | events |
|---|---:|
| k6 was told `accepted` | 4,593,000 |
| the gateway counted received | 4,593,000 |
| rows in Postgres | 4,593,000 |
| records in the dead letter | 0 |

The first two are both the gateway's own arithmetic — one from the response it
wrote, one from the counter it incremented — and a bug that lost events could
keep them consistent **with each other**. The database is the only one that did
not take our word for it.

**A run where the three disagree failed**, however good the percentiles are.

## The run above

`areteacademy/brevis-gateway:0.11.0`, Darwin arm64, 11 CPUs, Postgres 17 in a
container on the same kernel, `auto_table[columns]` with `write: append`:

| | |
|---|---:|
| events accepted | **4,593,000** (102,064/s) |
| body sent | 978 MiB in 45s |
| `503` (backpressure) | 0 |
| p50 / p95 / p99 | 9.2 ms / 29.0 ms / 46.9 ms |
| batches delivered | 766, none buried |

## The thresholds are the point

k6 exits non-zero when one fails, so this is a **gate** and not a chart:

| threshold | why |
|---|---|
| `p95 < 500ms` | the accept path must stay off the sink's latency. A request waiting for a `COPY` is the defect the async pipe exists against. |
| `http_req_failed < 1%` | a `503` is fine; a timeout or a 5xx is not. |
| `events accepted > 1000` | a threshold suite passes against a gateway that answered nothing. |

They are deliberately loose: this runs on whatever machine CI gave it. They
catch **an order of magnitude**, which is what a regression looks like, not a
percent.

**A `503` is not a failure.** It is the gateway refusing an event it has
nowhere to put, and it is safe to retry — the `ingestion_id` is a frozen
function of the event, so the same body sent again is the same record. Counting
it as an error would make backpressure look like a bug.

## What it does not measure

- **Not BigQuery.** The destination is a Postgres in a container. A warehouse's
  load-job quota, commit latency and DDL concurrency are not in these numbers
  and cannot be.
- **Not durability.** `durability: memory` is the only tier, so a `202` means
  *accepted into RAM*. This measures how fast that happens, never what survives
  a `SIGKILL`.
- **Not your hardware.** Read it as a shape and a ratio.

## The local cloud, by profile

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

One file with profiles, not one per cloud: two files are two project names, and
`postgres` in both already replaced the engine's development database once.

### floci covers less of BigQuery than it looks

Measured, not assumed:

| | |
|---|---|
| create a dataset and a table | ✅ |
| `timePartitioning` and `clustering` preserved | ✅ — the shape of the `0.9.1` bug |
| `PATCH` a column | ✅ — `auto_table`'s evolution |
| `insertAll` | ✅ |
| queries | ✅, only with `/var/run/docker.sock` mounted |
| **load jobs** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

The SDK writes to BigQuery **only** through load jobs. So floci exercises every
line of `auto_table` that turns a payload into DDL — and **not one Brevis
write**. That is still the half with no local test today, and it is worth
having. But a green suite against floci does not mean BigQuery works.

[k6]: https://k6.io
