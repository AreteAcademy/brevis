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
POST /v1/clicks  →  decode  →  hook  →  ingestion_id  →  batch  →  Pub/Sub
```

### What it does

**Configured by a file.** Streams, paths, formats, identity, retry, sink and
dead letter, and every field refused when it cannot be honoured — a gateway
that starts on a config it half understood drops events for a reason nobody can
see.

**Two sinks**: `pubsub` and `files`, the latter reading a directory, `gs://`
and `s3://`. Both are the SDK's own drivers, driven directly: `core.Writer`
takes a batch, which is exactly what a micro-batching sink wants, so they
arrive already tested and already refusing what they cannot do.

**The hook is Go, compiled in**, named by the YAML. 93 ns/op against Starlark's
959 and yaegi's 1,281, for nothing added to the binary. Returning nil drops the
event; a panic is recovered per event and never takes the batch down.

**`ingestion_id` on every record**, the frozen UUID v5 the whole product shares.
A client that retries a `POST` produces the same id, so a sink with dedup
absorbs the retry — and a row landed here is the same row a batch fetcher would
land for the same record.

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

### Not yet

Counters, a disk buffer, and the other four sinks the SDK already carries. None
of them is in the way of using it; all of them are in the plan.
