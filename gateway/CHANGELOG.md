# Changelog — the gateway

`areteacademy/brevis-gateway`, published from a `gateway/v*` tag.

Its own version, on purpose. The gateway is a separate Go module with a
separate maturity, and numbering it with the engine would claim one it does not
have — the engine is at 0.15 and this is at 0.1, which is the true statement.
The engine's versions are in [`CHANGELOG-motor.md`](../CHANGELOG-motor.md) and
the Go SDK's in [`CHANGELOG.md`](../CHANGELOG.md).

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
the versions follow [SemVer](https://semver.org/).

---

## [0.1.0] — 2026-09-24

The first one. An HTTP endpoint that lands data.

```
POST /v1/clicks  →  decode  →  hook  →  ingestion_id  →  batch  →  202
                                                          ↓
                                              workers  →  Pub/Sub | Postgres | files
```

### What it does

**Configured by a file.** Streams, paths, formats, identity, retry, sink and
dead letter, and every field refused when it cannot be honoured — a gateway
that starts on a config it half understood drops events for a reason nobody can
see.

**Three sinks**: `pubsub`, `postgres` and `files`, the last reading a
directory, `gs://` and `s3://`. All three are the SDK's own drivers, driven
directly: `core.Writer` takes a batch, which is exactly what a micro-batching
sink wants, so they arrive already tested and already refusing what they cannot
do. An unknown `type:` is refused at load, naming the three that exist.

**Postgres writes are `append` or `merge`, and `write` is required.** `append`
is `COPY FROM STDIN`, Postgres's fast path, and every delivery lands. `merge`
stages the batch in a `TEMP TABLE … ON COMMIT DROP` and runs
`INSERT … ON CONFLICT (ingestion_id) DO NOTHING` in **one transaction**, so the
first delivery of an event wins and a crash mid-way leaves the table as it was.
It needs a `UNIQUE` index on `ingestion_id` and refuses without one rather than
silently appending. `upsert` — `DO UPDATE`, last delivery wins — is refused **by
name**, because accepting the word and behaving like `merge` is the failure the
field exists to prevent.

**The hook is Go, compiled in**, named by the YAML. 93 ns/op against Starlark's
959 and yaegi's 1,281, for nothing added to the binary. Returning nil drops the
event; a panic is recovered per event and never takes the batch down.

**`ingestion_id` on every record**, the frozen UUID v5 the whole product shares.
A client that retries a `POST` produces the same id, so a sink with dedup
absorbs the retry — and a row landed here is the same row a batch fetcher would
land for the same record.

**Delivery is asynchronous and bounded.** A request appends to a buffer and
returns; a worker pool delivers. No caller waits for a publish, a `COPY`, or a
sink that is down — before this, whoever happened to fill a batch paid the full
round trip and, on an outage, the full retry window. The queue has a size and
the buffer has a ceiling: past them the gateway answers `503` with
`Retry-After` rather than accepting an event it has nowhere to put. The `503`
is safe to act on, because the `ingestion_id` makes the same body the same
record.

**A refused batch is retried and then buried**, with the reason, the sink and
the time on every record. A stream with no `dead_letter` is refused at load:
defaulting it to silence would put the decision where nobody makes it.

**Bearer authentication**, required outside `BREVIS_ENV=local`, where the
process refuses to start without it. An ingestion endpoint is a write endpoint
on somebody's topic; an unauthenticated one on a routable address is an open
relay.

### What it answers, and what that means

**`202`, not `200`.** With `durability: memory` — the only tier — that is the
whole of what it can honestly claim. A crash loses what is in flight. `disk`
and `synchronous` are refused **by name** rather than accepted and treated as
memory, because somebody writing `disk` believes their events survive a crash.

### What this image is

**A gateway with no hooks**, which is the honest artifact for a compiled-hook
design: it serves streams that declare no `hook:`. A hook of your own means a
binary of your own, which is ten lines around `gateway.Main` — the same
arrangement the SDK asks for, and `gateway/example` is a working one.

**A clean stop delivers what is buffered**, through the same path a full batch
takes: retried, and buried with the reason when the sink will not have it.
Writing straight to the sink at shutdown was a hole — a batch it refused was
lost, no retry and no dead letter, on the ordinary path of every deploy. A
drain that outlasts its window is reported rather than waited on.

### Not yet

Counters, a disk buffer, `upsert`, and the sinks the SDK already carries but
this does not wrap yet — BigQuery, MySQL, Redshift. None of them is in the way
of using it; all of them are in the plan.
