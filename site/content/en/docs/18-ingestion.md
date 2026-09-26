---
title: Ingestion
description: An HTTP endpoint that lands data — validate, identify, batch, deliver. Its own image, its own version.
group: Ingestion
order: 18
slug: ingestion
---

The ingestion gateway is an **HTTP endpoint that lands data**. A client `POST`s,
gets a `202`, and what arrived becomes a record on a topic, in a table, or in a
bucket.

```
POST /v1/clicks  →  decode  →  hook  →  ingestion_id  →  batch  →  202
                                                          ↓
                                            workers  →  Pub/Sub | Postgres | …
```

It is **a different binary, a different Go module, a different version and a
different image**. The engine orchestrates and never touches customer data; the
gateway does nothing else. The numbering is independent on purpose: the engine
is at 0.15 and the gateway at 0.8, which is the true statement.

```bash
docker run -p 8080:8080 \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.8.0-slim
```

## The file is the contract

There is no code to write except the hooks. Streams, paths, formats, identity,
retry, destination and dead letter — all in the YAML, and every field refused
when it cannot be honoured.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` is declared and never guessed: guessing is how a batch of a thousand
becomes one row holding an array.

## The `ingestion_id`, which is the point

Every record leaves with an `ingestion_id`: a **frozen UUID v5** over
`provider|entity|source_key|record_ts`.

It is the **same id** a batch fetcher computes for the same record. A row the
gateway lands and a row an [SDK](/docs/sdk/) pipeline lands are *one row*,
with no reconciliation between them.

Two practical consequences:

- **A retried `POST` is the same record.** A client can send again without fear;
  a destination with `merge` absorbs the retry instead of writing a second row.
- **The formula is frozen.** All four fields are required, and leaving one out
  produces a *different* id rather than a weaker one. The file refuses the
  incomplete configuration instead of accepting it.

Ingestion tools move bytes very well and have no opinion about what a record
*is*. That opinion is what the gateway adds.

## The request never waits for the sink

It decodes, runs the hook, computes the identity, appends to a buffer and
returns. **That is all.** A full batch goes to a worker pool, and the publish,
the `COPY`, the retries and the dead letter all happen there.

This is not cosmetic. Before it existed, the request that happened to fill a
batch ran the delivery inline — so one caller in every `flush.records` paid the
full round trip, and when the sink was down, the full retry window. A p99 shaped
by which caller was unlucky is not a p99 anybody can act on.

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### Why there is a size trigger

The other two are **counts**, and a count cannot tell 500 events of 2 KB from
500 of 2 MB — 1 MB against 1 GB, the same number in the file. `max_records:
10000` has the same blind spot: 20 MB or 20 GB, and nothing in the YAML could
say which.

A producer who starts sending the whole document instead of its id takes the pod
down, and until this existed **no field could have expressed the limit**.

Size is measured on what **arrived** — not on what the event weighs in memory (a
`map[string]any` is several times its JSON) and not after a hook that inflates
it. An approximation, and the right one: it is free at decode, it moves with the
payload, and the thing it guards against is a record that grew tenfold.

**On BigQuery `size` is a ceiling, not a target.** That destination allows 1,500
load jobs per table per day, which is why `every` has a 60-second floor. But the
floor governs the **timer**: a size that fires every few seconds walks straight
past it and spends the same quota. Nothing refuses that at load, because the
arrival rate is not knowable there. What exists instead is visibility:

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

`trigger="size"` climbing on a BigQuery stream is telling you the window is not
what is batching.

**It is bounded, not fire-and-forget.** A goroutine per batch would turn a sink
outage into unbounded memory. When the queue and the buffer are both full, the
answer is **`503` with `Retry-After`** rather than a `202` for an event with
nowhere to go. An accepted event that is never delivered is the one outcome this
service exists to not have.

And that `503` is **safe to retry**, in a way almost no service can claim: the
`ingestion_id` is a frozen function of the event itself, so the same body sent
again is the *same record*.

## When the sink refuses

The batch is retried — four attempts over roughly seven seconds by default,
doubling with jitter — and then it goes to the **dead letter**, with the reason
on every record:

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

The reason travels **on the record** and not only in a log, because whoever
finds that file later has the events and not the log — and *why is this here* is
their first question.

**A stream with no `dead_letter` is refused at load.** Defaulting it to silence
would put the decision where nobody makes it, and a refused batch that is only a
log line is losing data quietly.

## The hook is Go, compiled in

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

The YAML names a hook; it does not carry one. Measured before choosing: a Go
function costs **93 ns/op** against Starlark's 959 and yaegi's 1,281, for
nothing added to the binary — and `plugin.Open` is not an option at all, because
under `CGO_ENABLED=0`, which is how every artefact here is built, it returns
`plugin: not implemented`.

What it costs, said where somebody will read it: **adding a hook is a rebuild
and a deploy, not a config change.** That is the right trade while the hooks are
written by the people who ship the binary, and the wrong one the day a customer
has to change one without a release.

An error in a hook sends *that* event down the dead-letter path and never fails
the batch beside it. So does a panic: it is recovered per event, because one
malformed record must not take down a process serving every other stream.

## When an event is too big

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

The event is written to `archive` **whole**, and a reduced version continues into
the stream carrying a pointer back:

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

This is the **Claim Check** pattern, and it beats a flat `413` because a `413`
loses the event — and an oversized payload is usually the most interesting one
somebody has: it is the request with the whole document attached, which is the
case worth debugging.

`larger_than` measures one **event**, after the hook. `listen.max_body` is a
different thing: it caps a whole **request** and refuses with `413` before a
byte is parsed.

## What it counts

Eleven Prometheus series, on **its own port** — never the ingest one, and that
rule matters more here than it does in the engine: the ingest port is public by
construction, it is where clients `POST`, so a `/metrics` on it would publish
every stream name, path and destination to whoever found the path. Naming the
same address for both is refused at load.

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` is **not** prefixed with `brevis_`, and that is
deliberate: collectors look for exactly that name. It says when the process
started, and without it a collector that does start-time adjustment — the
OpenTelemetry Prometheus receiver, which Google Managed Prometheus is built on
— anchors every cumulative series at its **first scrape** and spends that
scrape's value as the baseline. The result is every counter reading low, with
the deltas right and the totals wrong.

The last three are the ones nobody asks for until after the first incident.
`saturated_total` is the only number that says a client was told to back off;
`buffer_records` against `buffer.max_records` is the only one that **predicts**
it.

`reason` is a closed set and never the error text: an error string carries a
table name, a column, sometimes a row, and one malformed client would mint a new
series per request. The text stays in the dead letter, on the record.

## `202`, not `200`

The gateway has **accepted** the events. With `durability: memory` — the only
tier implemented — that is the whole of what it can honestly claim. The tier
that earns a `200` is the one that has already written them down, and it does
not exist.

`disk` is refused **by name**, not accepted and treated as memory: somebody
writing `disk` believes their events survive a crash, and agreeing without
delivering is exactly the failure that field exists to prevent.

What is in a buffer at shutdown is delivered, not dropped — through the same
path a full batch takes, retried and buried.

## What it does not do

- **It does not appear in the console.** It shares no database with the engine's
  interface, so there is no `/ingestion` page — and the schema evolution
  `auto_table` performs does not show up there either. That is the next step and
  it has not been written.
- **A stream with a `table` does not evolve schema.** When you name the table,
  the contract is the table's: a field it does not have is refused with the
  message that fixes it, rather than altering a table somebody reviewed. Growing
  a column on its own is what `auto_table` does, and it is the difference
  between the two.
- **A stream with a `table` creates no table and no index.** A service that
  creates tables turns a typo into a second table nobody is reading — which is
  why creation happens only under `auto_table`, where `naming` bounds which
  names may exist and `listen.auth` is mandatory.
- **The published image has no hooks**, which is the honest artefact for a
  compiled-hook design: it serves streams that declare no `hook:`.

## One route, N tables

`auto_table` routes each event to the table the **envelope** names, creates that
table if it is absent, and grows a column when a new field turns up:

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

Control fields at the top, the record inside `data`, and seven `brevis_*`
tracking columns on every table. One route, N tables, nothing declared.

It is in [Ingestion sinks](/docs/ingestion-sinks/#one-route-n-tables-nothing-declared).

## Where to go next

| if you want | go to |
|---|---|
| the destinations and how each one writes | [Ingestion sinks](/docs/ingestion-sinks/) |
| the `ingestion_id` from the other side | [Go SDK](/docs/sdk/) |
| dashboards and alerts | [Observability](/docs/observability/) |
