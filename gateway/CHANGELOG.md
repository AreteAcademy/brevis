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

## [0.2.0] — 2026-09-24

### The sinks are injectable, and there is a slim image

A gateway that only ever writes to Postgres carried the AWS SDK, the Google
client stack and Arrow: **48.9 MB against 10.0** for the same gateway with only
what it uses. Nothing was wrong with the linker — it prunes what nothing
references — the problem was one `switch` that named every constructor, so
every driver was referenced in every build.

Each sink is now its own package and the binary registers what it carries:

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

**The import list is the selection.** That is how `database/sql` has always
worked, and it was already the rule one level down: `sdk/store/s3` says
*"importing it costs you the AWS SDK"* precisely so a fetcher reading GCS does
not pay for it. Anyone with a hook is compiling their own binary already, so for
them this costs nothing at all.

Object stores are a second registry, because a scheme is not a destination:
`files` is one sink that writes to a directory, to `gs://` and to `s3://`, and a
build that only ever writes locally should not carry the AWS SDK to do it.

### Two images

| | sinks | size |
|---|---|---|
| `areteacademy/brevis-gateway:0.2.0` | all six, both stores | 48.6 MB |
| `areteacademy/brevis-gateway:0.2.0-slim` | postgres, local `files` | **12.4 MB** |

Built from one tree in one job, because two images from two checkouts is how a
`-slim` tag ends up a commit behind the one it claims to match. No
`latest-slim`: `latest` is already a tag nobody should deploy, and a second
floating one would be a second way to be surprised.

### Refusals now report per binary

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

A fixed list would have sent that operator looking for a config mistake they did
not make. Same for an object store: a `s3://` dead letter in a binary with no S3
backend is refused **at startup**, naming the scheme — rather than on the first
batch it has to bury, which is the failure the whole split prevents.

The per-driver checks moved into the drivers with them, so `config.go` no longer
claims to know which sinks exist. Both still run before the listener opens.

### A test that watches the weight

`go list -deps` on both mains, asserting the slim build reaches neither the AWS
SDK nor the Google stack nor Arrow. It is the one claim here no ordinary test
can see: everything compiles and every test passes whether or not the linker
pruned anything, and the cost shows up only as a number in a `docker pull`. An
import added to the root package that drags one of them back in fails this and
nothing else.

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

**Six sinks**: `pubsub`, `postgres`, `mysql`, `bigquery`, `redshift` and
`files`, the last reading a directory, `gs://` and `s3://`. All six are the
SDK's own drivers, driven directly: `core.Writer` takes a batch, which is
exactly what a micro-batching sink wants, so they arrive already tested and
already refusing what they cannot do. An unknown `type:` is refused at load,
naming the six that exist.

Four are proven end to end against the real thing — Pub/Sub on its emulator,
Postgres and MySQL on real servers, `files` on the filesystem and on S3.
**BigQuery and Redshift are not**: neither has a local emulator, so what is
tested here is the config and the refusals, and the docs say so rather than
letting "runs" carry a claim nobody checked.

**A `files` sink pointed at `gs://` or `s3://` now works.** It did not: the
driver takes the object-store backend as a field and the gateway passed none,
so a bucket path failed at write time, after the pod had gone ready. For the
DEAD LETTER that meant "the dead letter refused them too, and they are lost" --
the one outcome it exists to prevent. Credentials are now resolved at startup,
and `AWS_ENDPOINT_URL_S3` points S3 at MinIO, Ceph or R2.

**Every table sink takes `append` or `merge`, and `write` is required.** One
word, one meaning, in all four: `merge` is the idempotent insert and the FIRST
delivery wins -- `ON CONFLICT DO NOTHING` in Postgres, `INSERT IGNORE` in
MySQL, `MERGE … WHEN NOT MATCHED` in BigQuery and Redshift.

**Postgres in particular.** `append`
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
