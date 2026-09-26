# The benchmark

```bash
./bench/run.sh                                                  # the local build
BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
```

It brings up a Postgres, starts the gateway, loads it with [k6], waits for the
buffer to drain, scrapes the gateway's own `/metrics`, and writes
[`results/REPORT.md`](results/REPORT.md).

[k6]: https://k6.io

## Why two sources, and then a third

k6 knows what a producer saw. The gateway knows what it did. A load test
carrying only the first cannot tell a fast gateway from one that answers `202`
and drops what it accepted — so the report puts `events accepted` next to
`batches delivered` and `records buried`.

Then it counts the rows in Postgres, and that one matters most. The first two
are both the gateway's own arithmetic — one from the response it wrote, one
from the counter it incremented — and a bug that lost events could keep them
consistent with each other. The database is the only witness that did not take
our word for it.

A run where the three disagree **failed**, however good the percentiles look.

## The thresholds are the point

k6 exits non-zero when one fails, so this is a gate and not a chart:

| threshold | why |
|---|---|
| `http_req_duration p95 < 500ms` | the accept path must stay off the sink's latency. A request waiting for a `COPY` is the defect the async pipe was built against. |
| `http_req_failed < 1%` | a `503` is fine; a timeout or a 5xx is not. |
| `events accepted > 1000` | a threshold suite can pass against a gateway that answered nothing. |

They are deliberately loose. This runs on whatever machine CI gave it, against
a container on the same kernel: it catches an order of magnitude, which is what
a regression looks like, not a percent.

**A `503` is not a failure.** It is the gateway refusing an event it has
nowhere to put, and it is safe to retry — the `ingestion_id` is a frozen
function of the event, so the same body sent again is the same record. Folding
it into `http_req_failed` would make backpressure look like a bug.

## Knobs

| variable | default | |
|---|---|---|
| `BREVIS_BENCH_IMAGE` | *(local build)* | the published image is the number worth quoting |
| `BREVIS_BENCH_VUS` | `16` | concurrent producers |
| `BREVIS_BENCH_PER_REQUEST` | `200` | events per `POST`, as ndjson |
| `BREVIS_BENCH_HOLD` | `30s` | how long to hold at full load |
| `BREVIS_BENCH_TABLES` | `1` | distinct tables, to exercise `auto_table`'s routing |

## What it does not measure

- **Not BigQuery.** The destination is a Postgres in a container. A warehouse's
  load-job quota, commit latency and DDL concurrency are not in these numbers
  and cannot be — see [the emulation note](#emulators) below.
- **Not durability.** `durability: memory` is the only tier, so a `202` means
  *accepted into RAM*. This measures how fast that happens, never what survives
  a `SIGKILL`.
- **Not your hardware.** Read the report as a shape and a ratio.

## Emulators

`docker-compose.drivers.yml` carries the local stack, by profile:

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d       # floci
docker compose -f docker-compose.drivers.yml --profile metastore up -d # redis, memcached
docker compose -f docker-compose.drivers.yml --profile all up -d
```

**floci covers less of BigQuery than it looks.** Measured, not assumed:

| | |
|---|---|
| create a dataset and a table | ✅ |
| `timePartitioning` and `clustering` preserved | ✅ — the shape of the `0.9.1` bug |
| `PATCH` a column onto a table | ✅ — `auto_table`'s evolution |
| `insertAll` | ✅ |
| queries | ✅, but only with `/var/run/docker.sock` mounted |
| **load jobs** | ❌ — `/jobs` answers `Only QUERY jobs are supported`, and the upload route the Go client actually uses answers `405` |

The SDK writes to BigQuery **only** through load jobs. So floci can exercise
every line of `auto_table` that turns a payload into DDL, and **not one Brevis
write**. That is still the half with no local test today, and it is worth
having — but a green suite against floci does not mean BigQuery works.

**floci leaves an orphan.** Queries run in a sibling container that floci
starts through the docker socket, so `compose down` does not remove it —
Compose only removes what it created:

```bash
docker rm -f floci-gcp-bigquery-duck
```

Kafka, RabbitMQ and Mongo are in the compose on their own profiles, and there
is **no Brevis driver for any of them**. They are there so the driver that
needs one can be written against something. A compose service is not a feature.
