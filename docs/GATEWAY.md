# The ingestion gateway

An HTTP endpoint that lands data. `POST` an event, it is shaped by a hook,
given an identity, batched, and delivered to a destination.

```bash
docker compose up -d pubsub topic gateway

curl -X POST http://localhost:8090/v1/clicks \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"e-42","occurred_at":"2026-09-24T12:00:00Z",
       "host":"acme.example.com","region":"sa-east-1","amount":99.9}'
# → 202 {"accepted":1,"rejected":null}
```

What the subscriber then receives:

```json
{
  "amount": 99.9,
  "event_id": "e-42",
  "host": "acme.example.com",
  "ingestion_id": "7aa2549c-0503-5f93-ab58-0ab78e7eb555",
  "occurred_at": "2026-09-24T12:00:00Z",
  "region": "sa-east-1",
  "tenant": "acme"
}
```

attributes: `{region: sa-east-1, tenant: acme}`

The payload is what was sent plus what the hook added. Nothing Brevis-shaped
wraps it, and only the attributes the file named travel beside it: the topic's
contract belongs to whoever owns the topic.

## It is not the engine, and shares nothing with it

No database, no queue, no migration, no console. That is the design and not an
omission — `engine-weight.sh` states the rule both follow:

> *the data drivers live in the TASKS' pods, which are other images*

The engine orchestrates and never touches customer data. The gateway does
nothing else, on every request. They are separate modules, separate binaries,
separate images, and the engine's package count is unchanged by the gateway
existing.

| | engine | gateway |
|---|---|---|
| shape | batch, scheduled | online, per request |
| unit | a run | an event |
| the SLO | did the run finish | p99 of `POST`, nothing lost on a 200 |
| scaling | one scheduler | N stateless replicas |

## Why there is nothing in the console

**There is nothing to see today.** The console reads the engine's Postgres, and
the gateway never writes to it.

Making it appear there is possible, and the shape matters more than the effort:

- **The gateway writing to the engine's database.** Cheap to build and the worst
  of the options: it couples an always-on data plane to the orchestrator's
  store. The question it forces — *does the gateway stop accepting when that
  Postgres is down?* — has no good answer.
- **The console querying the gateways over HTTP.** Which replica? They each hold
  their own buffer, so the answer is "all of them, and add up", and the console
  grows a service discovery problem.
- **Publishing the config, the way a workflow is published.** `brevis publish`
  already takes a file and stores a definition the console renders. A gateway
  config is a file of exactly that kind, and publishing is a deliberate act
  rather than a runtime dependency: the console shows what is *configured* to
  receive data, and the gateway keeps accepting whether or not that database is
  reachable.

The third is the one worth doing, and it is worth being clear about what it
gives: the **configuration**, not the traffic. Live counters — accepted,
rejected, delivered, buffered, sink failures — belong in `/metrics`, beside the
engine's, which is where an operator already watches for trouble. A console
page that showed a number a minute old would be worse than one that sends you
to the dashboard that updates.

Neither is built. Both are listed in the plan's build order, and neither is in
the way of using the gateway.

## Who may write

Required outside `BREVIS_ENV=local`, where the gateway **refuses to start**
without it — the same rule the engine follows, spelled with the same word.

```yaml
listen:
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS     # the variable, never the keys
```

```bash
curl -X POST http://gateway/v1/clicks \
  -H 'Authorization: Bearer <key>' -d '{...}'
```

An ingestion endpoint is a **write** endpoint on somebody's topic or table. An
unauthenticated one on a routable address is not a gateway with a gap, it is an
open relay — which is why the refusal is at boot and not a warning.

Locally it stays open, because asking for a token on every
`docker compose up` only teaches a team to turn authentication off.

`keys_from` names the environment variable, never the keys: the same split
`secrets:` makes in a workflow, and for the same reason — the file is in git.
Every key in the list is accepted, which is what makes rotation possible: add
the new one, move the callers, remove the old.

`/health` is outside the guard. A readiness probe carries no credential, and one
that needed a token would report the gateway down whenever the token was wrong,
which is a different outage from the one it exists to see. Everything else
answers `401`, including paths that are not streams — otherwise an
unauthenticated caller learns which paths exist by reading the status codes.

## When the sink refuses

It is retried — four attempts over roughly seven seconds by default, doubling
with jitter — and then the batch goes to the **dead letter**, with the reason
attached to every record:

```json
{
  "event_id": "dl-9",
  "host": "x.y",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "… rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:brevis-local/nao-existe",
  "_dead_letter_at": "2026-09-24T22:02:32Z"
}
```

The reason travels **on the record** and not only in a log, because whoever
finds this file later has the events and not the log, and *why is this here* is
their first question. The `ingestion_id` travels too, so a replay lands where
the original would have.

**A stream with no `dead_letter` is refused at load.** Defaulting it to silence
would put the decision where nobody makes it — and a refused batch that is only
a log line is losing data quietly, which is the one failure this exists not to
have.

`files` reads a directory, `gs://` and `s3://`, so the dead letter is a folder
on a laptop and a bucket in production with no change to the gateway. In compose
it is a **volume**: written inside the image it would die with the container,
which is a slower way of losing the events it exists to keep.

## Where it writes

Three shapes of destination, and the list is deliberately explicit — a
connector that is *planned* and a connector that *runs* are different things to
somebody choosing this.

| Sink | State | What a write is | Proven against |
|---|---|---|---|
| `pubsub` | **runs** | one publish per batch, attributes and ordering key from the event's own fields | the emulator |
| `postgres` | **runs** | `COPY FROM STDIN`, or a staged `INSERT … ON CONFLICT` in one transaction | a real Postgres |
| `mysql` | **runs** | multi-row `INSERT` in a transaction, or `INSERT IGNORE` | a real MySQL |
| `files` | **runs** | NDJSON to a directory, `gs://` or `s3://` — also what the dead letter uses | the filesystem, and S3 |
| `bigquery` | **runs** | rows loaded, or staged and `MERGE`d on `ingestion_id` | **config only — no emulator exists** |
| `redshift` | **runs** | the batch to S3, then `COPY`, then `MERGE` | **config only — no emulator exists** |
| `sqlite`, `redis`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka`, `rabbitmq` | planned | nothing written | — |

An unknown `type:` is **refused at startup**, naming what *this binary* carries.
A gateway that starts on a sink it does not have is one that drops events for a
reason nobody can see.

**Two of the six are unproven end to end**, and the table says so rather than
letting the word "runs" carry a claim nobody checked. BigQuery and Redshift have
no local emulator, so what is tested is the config, the refusals and the driver
underneath — which is already covered by the SDK's own suite. The first
production use of either is the real proof, and it should be a low-stakes stream.

`gs://` and `s3://` are not separate connectors: `files` reads all three path
shapes, so the dead letter is a folder on a laptop and a bucket in production
with no change here. That needs credentials, and the gateway resolves them the
ordinary way — the pod's own identity, or `AWS_*` / `GOOGLE_APPLICATION_CREDENTIALS`.
Set `AWS_ENDPOINT_URL_S3` to point S3 at MinIO, Ceph or R2; the gateway switches
to path-style addressing whenever that variable is set, because virtual-host
addressing puts the bucket in the hostname and those servers do not answer to it.

**Credentials are resolved at startup, not on the first write.** A gateway whose
S3 role is wrong fails to go ready instead of going ready and losing the first
batch it tries to bury.

### Writing to MySQL, BigQuery and Redshift

The same two words, and they mean the same two things everywhere — `merge` is
the idempotent insert and the **first delivery wins**, in all four:

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | multi-row `INSERT` per block | `INSERT IGNORE` |
| **bigquery** | rows loaded (staged through GCS above the driver's inline limit) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | `COPY` from S3 | `COPY` to a staging table, then `MERGE` |

Postgres and MySQL both **require a `UNIQUE` index on `ingestion_id`** for
`merge` and refuse without one, rather than silently appending.

```yaml
# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials,
# which is what a workload identity is for. The name has no dots — the
# project and the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the
# cost of a block, so every batch becomes an object in `staging` and then a
# COPY. A stream flushing every second writes 86,400 objects a day — flush
# much wider here than anywhere else.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy   # a role, never a key
  write: merge
```

`iam_role` is a role and the driver will not accept a key at all: a key in a
`COPY`'s URL ends up in the cluster's query log, which plenty of people read.

### Writing to Postgres

```yaml
sink:
  type: postgres
  dsn_from: BREVIS_ORDERS_DSN     # the NAME of the variable, never the string
  table: landing.orders           # schema-qualified; it must already exist
  write: merge                    # required
```

`write` is **required and never defaulted**, because the two modes are
genuinely different and only the table's owner knows which one it is:

**`append`** is `COPY FROM STDIN` — Postgres's fast path, no per-row round trip
and no index lookup per row. Every delivery lands, a redelivery included. Right
for a log, wrong for a table somebody counts.

**`merge`** stages the batch in a `TEMP TABLE … ON COMMIT DROP`, `COPY`s into
it, and runs `INSERT … SELECT … ON CONFLICT (ingestion_id) DO NOTHING` — all
inside **one transaction**. A crash between any two of those steps leaves the
table exactly as it was. The **first** delivery of an event wins and a
redelivery is ignored, which is right for an event (it happened once) and wrong
for a row carrying a mutable state.

It needs a `UNIQUE` index on `ingestion_id`, and the driver **refuses without
one** rather than silently appending — without the index `ON CONFLICT` has
nothing to match, and every redelivery would duplicate into a table whose owner
asked for the opposite:

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

The refusal is not a dropped batch: it is retried, then buried in the dead
letter with the reason and the `CREATE INDEX` on the record, so nobody loses
events while somebody creates the index.

**`upsert` is refused by name.** `ON CONFLICT DO UPDATE`, where the *last*
delivery wins, is a real mode that is **not implemented** — it is a dedup mode
the SDK does not have, and adding one means every writer answers for it
explicitly or it becomes a silent append in whichever was missed. Accepting the
word while behaving like `merge` is the exact failure this field exists to
prevent, so the config says so instead.

What makes `merge` mean anything across products: the `ingestion_id` is the
**same id a batch fetcher computes** for the same record — the frozen UUID v5
over `provider|entity|source_key|record_ts`. A row this gateway lands and a row
a pipeline lands are one row, with no reconciliation between them.

No `Columns` declaration is sent. A pipeline declares them because its rows have
one shape it controls; a gateway's batch is whatever *N* clients posted in one
flush window. The driver resolves the column list from the table itself,
intersected with what the batch carries, **before** touching the server — so an
unknown field is refused with the message that fixes it instead of failing
mid-`COPY`, and a field one event omits is written as `NULL`.

## Which sinks a binary carries, and what that weighs

The sinks are **compiled in**, like the hooks, and the binary registers what it
has:

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

**The import list is the selection.** The Go linker prunes what nothing
references, so a binary that never imports the BigQuery driver does not carry
BigQuery — or Arrow, or the Storage Write API. That is how `database/sql` has
always worked, and it is the rule one level down too: `sdk/store/s3` says
*importing it costs you the AWS SDK*, precisely so a fetcher reading GCS does
not pay for it.

It is worth what it sounds like:

| build | packages | binary |
|---|---|---|
| all six sinks, both object stores | 864 | 48.9 MB |
| postgres + local `files` | 232 | **10.0 MB** |

Object stores are a **second** registry, because a scheme is not a destination:
`files` is one sink that writes to a directory, to `gs://` and to `s3://`, and a
build that only ever writes locally should not carry the AWS SDK to do it.

```go
stores := gateway.NewStores()
stores.MustRegister(s3.Scheme, s3.Open)      // only if a path needs it
gateway.Main(hooks, gateway.WithSinks(sinks), gateway.WithStores(stores))
```

### Two published images

| image | carries | size |
|---|---|---|
| `areteacademy/brevis-gateway:X` | six sinks, S3 and GCS, Redis and memcached | 55 MB |
| `areteacademy/brevis-gateway:X-slim` | `postgres`, `auto_table`, local `files` | **12.4 MB** |

Both from one build of one tree, so the two tags are always the same commit.
There is no `latest-slim` — `latest` is already a tag nobody should deploy.

**Want a different pair?** `gateway/cmd/gateway-slim` is fifteen lines and the
import block is the whole of its configuration. Copy it, change the imports,
build. Anybody who wants a hook is compiling their own binary already, so
choosing the sinks costs them nothing more.

### The refusal says which build you are holding

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

A fixed list — *"only pubsub, postgres, bigquery, mysql, redshift, files are
implemented"* — would send that operator looking for a config mistake they did
not make. The same holds for an object store: an `s3://` dead letter in a binary
with no S3 backend is refused **at startup**, naming the scheme, rather than on
the first batch it has to bury.

## One route, N tables, nothing declared

The producer POSTs an **envelope**: control fields at the top, the record
inside `data`.

```json
{
  "table_name": "app_orders",
  "unique_key": "id",
  "operation":  "INSERT",
  "description": "Pedidos do app",
  "data": {
    "id": "A-3",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| field | | |
|---|---|---|
| `table_name` | required | the table |
| `data` | required | the record, and **only** the record |
| `data[unique_key]` | required | `unique_key` defaults to `"id"` |
| `operation` | optional | `INSERT` \| `UPDATE` \| `DELETE`, default `INSERT` |
| `description` | optional | the control plane, **never** DDL |

The split is the point. `table_name` living beside `total` and `customer` was a
field of the *transport* pretending to be a field of the *record*, and
separating them is how every CDC format is shaped.

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^[a-z][a-z0-9_]{2,48}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: memory, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

### The seven columns every table carries

```sql
CREATE TABLE IF NOT EXISTS "landing"."app_orders" (
  "brevis_ingestion_id" TEXT NOT NULL UNIQUE,   -- UNIQUE only when write: merge
  "brevis_record_key"   TEXT NOT NULL,
  "brevis_operation"    TEXT NOT NULL,
  "brevis_received_at"  TIMESTAMPTZ NOT NULL,
  "brevis_loaded_at"    TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
  "brevis_stream"       TEXT,
  "brevis_gateway"      TEXT,
  ...                                           -- and the record's own
)
```

| column | from | |
|---|---|---|
| `brevis_ingestion_id` | computed | dedup of a redelivery |
| `brevis_record_key` | `data[unique_key]` | which **row** the event is about |
| `brevis_operation` | the envelope | CDC |
| `brevis_received_at` | **our** clock, at the 202 | **the partition column** |
| `brevis_loaded_at` | a database `DEFAULT` | the destination stamps it |
| `brevis_stream`, `brevis_gateway` | config | which route, which deployment |

`brevis_loaded_at` is a `DEFAULT` and not a value the gateway sends, and that is
deliberate: the gateway knows the **dispatch** time and the destination knows
the **write** time. The difference between the two columns is then the real
end-to-end latency, per row, with no instrumentation.

`brevis_received_at` and never a client's clock as the partition: a partition
column the client controls is a client that can write into 2035.

**`brevis_` is reserved.** A `data` carrying any key with it is refused per
event — otherwise a producer forges a control field, and a forged
`brevis_received_at` is worse than none because it looks real.

### The identity, with no clock in it

```
brevis_ingestion_id = uuid5(auto_table | table_name | data[unique_key] | sha256(canonical(data)))
```

| | |
|---|---|
| same record, same content, sent twice | same id → `merge` absorbs |
| same record, content changed (an UPDATE) | different id → both versions land |
| no clock in the formula | deterministic across deliveries |

There is no `occurred_at` in it, and that is why. Arrival is the gateway's
responsibility now, so a clock in this formula would be `time.Now()` — different
on every delivery, and every retry would be a new event.

The caveat is the documented one: two genuinely distinct events with
byte-identical `data` collapse. **For CDC that is correct** — two identical
updates to one record with the same values are the same fact.

### UPDATE and DELETE are recorded, not applied

A landing table is history. `brevis_operation` is a **column**; resolving the
current version is the downstream model's job:

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

Which is what Debezium, Fivetran and Airbyte all do — and it means `operation`
costs **nothing** in the write path: no upsert mode, no lock, no delete. It also
protects the one thing a landing table is for: applying an UPDATE in place loses
the previous version, and the day you want it is the day something broke.

### Two shapes, one contract

```yaml
shape: document   # the record goes whole into one JSON column
shape: columns    # each field gets a column
```

The producer's envelope never changes. What changes is what the operator's table
looks like, which is their decision and not the producer's.

| | `document` | `columns` |
|---|---|---|
| a new field | a new key — **no DDL, ever** | a new column: DDL, quota, a stream reopen |
| querying it | `data->>'novo_campo'` | `novo_campo` |
| a poison batch | impossible | possible, and refused per event |

**Types, in `columns`: scalar → `STRING`, object or array → `JSON`. No
inference, ever.** `null` has no shape and becomes `STRING`; `true`/`false` are
scalars and become `STRING`, which is the one that most invites an exception and
does not get one.

What it costs: no partition pruning on a date inside the record, no numeric
aggregation without a cast, and every query casting. Typing a column is the
**promotion** path — a human writing it in the YAML, reviewed in a diff.

A field's **name** has to match `[A-Za-z_][A-Za-z0-9_]{0,127}` — BigQuery's
rule, which is the narrowest of the four. Postgres would accept almost anything
quoted, and that is the trap: the table is created there, the producer works for
months, and it breaks the day somebody points a stream at BigQuery.

### The table grows a column on its own

```
POST {"data": {"id": "A-1", "total": 150}}                        → id, total
POST {"data": {"id": "A-2", "total": 150, "novo_campo": "x"}}     → id, novo_campo, total
POST {"data": {"id": "A-3", "total": 150, "novo_campo": "y"}}     → unchanged
```

**Additive, and only additive.** A field the record grew becomes a column;
nothing is ever dropped or narrowed, and the rows already written keep `NULL`
there.

A batch that loses the race to `ALTER` fails, and the pipe's **ordinary retry**
resolves it: four attempts over roughly seven seconds, and by the second the
winner's column is in the catalogue. That is the buffer — there is no second
one, because a batch waiting for DDL and a batch waiting for a worker are the
same thing.

**What is not solved yet**: the metadata quota with many replicas. Ten detecting
one new field is ten `ALTER`s, and BigQuery allows five per table per ten
seconds. That needs a shared debounce, which needs a shared metastore, which is
the next piece.

A field that arrives scalar and later arrives as an object is **refused to the
dead letter**: that is not a new column and not a widening, and serialising the
object into the `TEXT` column would leave it holding two types.

### The name is the attack surface

`auto_table` turns a string in a payload into DDL, so every field under
`naming` is a refusal:

- **`pattern`** defaults to `^[a-z][a-z0-9_]{2,48}$` — the intersection of what
  Postgres, MySQL, BigQuery and Redshift accept unquoted.
- **`allow`** narrows further, by prefix.
- **`max_new_per_hour`** (default 20) bounds **creations** in a rolling hour,
  never writes. **Per replica**: the budget lives in the process, so four
  replicas admit four times the number. Sharing it needs a shared metastore —
  set this to the deployment's budget divided by the replica count, or treat it
  as the circuit breaker it is.

A name outside the rules is refused **per event**, in the response, and every
well-formed event in the same request still lands. Per event and not per batch,
deliberately: refusing at write time would bury the events of every other
producer in the same flush window.

**`auto_table` is refused on an endpoint with no `listen.auth`.** A producer
that can name a table can create one. A NetworkPolicy does not cover it: that
limits who reaches the port, not which table name they ask for.

### The metastore: a cache, and a coordinator

```yaml
metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
```

Three backends: **`memory`** (the default, needs nothing), **`redis`** and
**`memcached`**. Four primitives, and the set is the intersection of what all
three can do atomically — anything richer would work on one and be emulated
badly on the others.

| | |
|---|---|
| `Get` / `Put` | what is known about a table. **May be wrong.** |
| `Claim` | set-if-absent with an expiry: the **debounce** |
| `Incr` | the rolling-window counter behind `max_new_per_hour` |

**`memory` is right with one replica and wrong with several**, and the
difference is the whole reason the other two exist. With `memory` each replica
has its own: the debounce debounces nothing and `max_new_per_hour` bounds a
*process*. Ten replicas meeting one new field would issue ten `ALTER`s against
BigQuery's five metadata operations per table per ten seconds, and the quota
would be gone in a second.

`addr_from` names the **environment variable** holding the address, never the
address — it carries a password often enough, and this file is in git.

#### Claim is a debounce, not a lock

Nothing is released and no lease is renewed. A replica that dies holding one
costs three seconds to the batches behind it, and they are batches being
*retried* rather than workers blocked.

The window is three seconds, bounded from both sides: long enough to collapse a
burst into one attempt, short enough that a loser gets through on its own
retries — the pipe backs off 500ms, 1s, 2s, so the third attempt is past the
window even if the winner never comes back.

Losing it returns an error and the pipe retries the batch. Waiting would hold
a worker for the window, and there are four of them.

This is what the market does: Delta Lake and Iceberg commit optimistically and
retry on conflict, Kafka Connect gets serialisation free from partition
ordering, Fivetran has one writer per table. Nobody takes a lock.

#### The cache is never the authority

The destination settles whether a table exists, and *N* replicas racing to
create one is the normal case — `AlreadyExists` is success. If the cache says
the column is there and the write fails, **the write is right**: the entry is
dropped and the next batch re-reads. Without that, a table recreated outside
the gateway leaves every replica lying until the TTL.

And a metastore that is **down** must not be able to fail a write. A `Get` that
errors is a miss; a `Claim` that errors behaves as if it were won; an `Incr`
that errors relaxes the rate limit. A cache that can stop ingestion is worse
than no cache.

#### What it costs

| build | |
|---|---|
| `areteacademy/brevis-gateway` | carries both — 55 MB |
| `-slim` | carries neither — **10.2 MB** |

The backends are packages, like the sinks and the object stores, so a binary
that never imports one does not carry it. A build that names `redis` in its
YAML and did not compile the client is refused **at startup**, by name.

### BigQuery has a flush floor

**1,500 load jobs per table per day.** The default one-second window is 86,400 —
57× the quota, gone in about twenty-five minutes. A stream that writes to
BigQuery, directly or through a router, is **refused at load** with a window
under 60s. `auto_table` cannot route into **Redshift**: that driver creates no
tables.

## The request never waits for the sink

A request decodes the body, runs the hook, computes the identity, appends to a
buffer and returns. **That is all it does.** A full batch is handed to a worker
pool, and the publish, the `COPY`, the retries and the dead letter all happen
there.

This is not cosmetic. Before it, the request that happened to fill a batch ran
the delivery inline — so one caller in every `flush.records` paid the full round
trip, and when the sink was down, the full retry window. A p99 shaped by which
caller was unlucky is not a p99 anybody can act on.

```yaml
buffer:
  flush: {every: 1s, records: 500}
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
```

**It is bounded, not fire-and-forget.** A goroutine per batch would turn a sink
outage into unbounded memory and a thundering herd on the way back up. So the
queue has a size, and when it and the buffer are both full the gateway answers
**`503` with `Retry-After`** rather than accepting an event it has nowhere to
put. An accepted event that is never delivered is the one outcome this service
exists to not have.

The `503` is **safe to act on** in a way it is not for most services: the
`ingestion_id` is a frozen function of the event's own fields, so sending the
same body again produces the *same record*, not a second one — and a `merge`
sink absorbs it. Retrying a `503` here cannot create a duplicate.

Shutdown waits for what is in flight, and a drain that outlasts its window is
**reported** rather than waited on: one batch can hold the full retry window,
and a shutdown that blocks past its deadline is a pod the orchestrator kills —
which loses the events anyway and says nothing about why.

## What it counts

Prometheus exposition, on **its own address**, and that rule matters more here
than it does in the engine: the gateway's port is public by design — it is where
clients POST — so a `/metrics` on it would publish every stream name, path and
destination to whoever finds the path. Naming the same address for both is
refused at load.

```yaml
metrics:
  addr: :9090     # leaving it out serves them anyway; addr: "" turns them off
```

Leaving it out still serves them, because an ingestion service nobody can see is
the failure this field exists against. `addr: ""` is how you turn them off, and
that is a decision somebody made rather than one they forgot.

```
brevis_gateway_events_received_total{stream,format}       counter
brevis_gateway_events_rejected_total{stream,reason}       counter
brevis_gateway_events_dropped_total{stream}               counter   a hook returned nil
brevis_gateway_oversized_total{stream}                    counter
brevis_gateway_batches_total{stream,sink,outcome}         counter   delivered|retried|buried
brevis_gateway_dead_letter_records_total{stream,sink}     counter
brevis_gateway_saturated_total{stream}                    counter   the 503s
brevis_gateway_delivery_seconds{stream,sink}              histogram retries included
brevis_gateway_batch_records{stream}                      histogram
brevis_gateway_buffer_records{stream}                     gauge
brevis_gateway_queue_batches{stream}                      gauge
```

The last three are the ones nobody asks for until after the first incident.
`saturated_total` is the only number that says a client was told to back off;
`buffer_records` against `buffer.max_records` is the only one that **predicts**
it.

**`reason` is a closed enum and never the error text** — `hook_error`,
`no_source_key`, `no_record_ts`, `identity`, `oversize`. An error string carries
a table name, a column, sometimes a row, and one malformed client would mint a
new series per request. The text belongs in the dead letter, on the record,
where whoever has the events can read it.

Written by hand, with no dependency: the OpenTelemetry SDK costs 40 packages
here and `prometheus/client_golang` 43, to report eleven instruments whose
format is `name{label="value"} 42` and has not changed in a decade. The slim
build exists to not pay for what it does not use.

## When an event is too big

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

The event is written to `archive` **whole**, and a reduced version continues
into the stream carrying a pointer back:

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
somebody has: the request with the whole document attached, which is the case
worth debugging. The claim check is *stamped* rather than left implicit;
agreeing out of band that the id is also the object's name works until somebody
changes the prefix, and then nothing says where to look.

`larger_than` measures one **event**, after the hook. `listen.max_body` is a
different thing: it caps a whole **request** and refuses with `413` before a
byte is parsed.

The caller is told, in the response:

```json
{"accepted": 0, "rejected": null, "archived": 1}
```

Only when there is one, so the field appearing means something happened rather
than being a zero everybody scrolls past.

**`hook` is required for the event to continue.** Which fields are heavy is
domain knowledge — a screenshot, a base64 attachment, a vendor's raw response —
and a YAML file cannot hold it. Without one the event is archived and **dropped
from the stream**: not lost, because the archive has it whole, and counted,
because an event that silently stops arriving is the worst outcome here.

The archive write is the one piece of I/O on a request goroutine, and that is a
deliberate exception. The reduction has to happen *after* the archive — reducing
first means a failed archive leaves a record pointing at an object that does not
exist — and the path is exceptional by construction. If it is hot, the limit is
wrong.

## When the row should say when it arrived

```yaml
stamp_loaded_at: true
```

Writes `ingestion_loaded_at`: when the gateway received it, UTC, RFC3339. The
SDK's column and the SDK's format, so a row this gateway lands and a row a
pipeline lands are the same shape.

Opt-in, because it adds a field to every record and both a topic's subscribers
and a table's columns notice.

## Configuration

See [`gateway/example/gateway.yaml`](../gateway/example/gateway.yaml). Every
field is refused when it cannot be honoured, and two refusals are worth knowing:

**`buffer.durability` accepts only `memory` today**, and refuses `disk` **by
name** rather than accepting the word. Somebody writing `disk` believes their
events survive a crash, and behaving like `memory` while agreeing would be the
one failure that field exists to prevent.

**`identity` requires all four fields.** The `ingestion_id` is a UUID v5 over
`provider|entity|source_key|record_ts` and the formula is frozen, so leaving one
out produces a *different* id rather than a weaker one.

**Every table-shaped sink requires `write`**, and refuses `upsert` by name. See
[Writing to Postgres](#writing-to-postgres).

These, and every other per-driver rule, are checked by the **driver** and not by
the parser — because which drivers exist is a property of the binary now. Both
run before the listener opens.

## The hook is Go, compiled in

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks)

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil          // returning nil DROPS the event, on purpose
}
```

The YAML names a hook; it does not carry one. Measured before choosing: a Go
function is 93 ns/op against Starlark's 959 and yaegi's 1,281, for nothing added
to the binary — and `plugin.Open` is not an option at all, because under
`CGO_ENABLED=0`, which is the build every artifact here ships with, it returns
`plugin: not implemented`.

### The published image has no hooks

`areteacademy/brevis-gateway` is a gateway with none registered, which is the
honest artifact for a compiled-hook design: it serves streams that declare no
`hook:`, and refuses to start on one that does, naming how hooks get there.

A hook of your own means a binary of your own, and it is ten lines around
`gateway.Main` — the same arrangement the SDK asks for, so both products have
one idiom rather than two. `gateway/example` is a working one.

What it costs, where somebody will read it: **adding a hook is a rebuild and a
deploy, not a config change.** That is the right trade while the hooks are
written by the people who ship the binary, and the wrong one the day a customer
has to change one without a release.

## 202, not 200

The gateway has **accepted** the events. With `durability: memory` that is the
whole of what it can honestly claim — the tier that earns a `200` is the one
that has written them down, and it is not built yet.

What is in a buffer at shutdown is delivered, not dropped: `memory` already
loses on a crash, and losing on a clean stop as well would make the tier useless
rather than merely limited.
