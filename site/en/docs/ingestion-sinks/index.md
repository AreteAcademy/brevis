# Ingestion sinks

> The six destinations, how each database writes a record, and why the image carries only what you use.

*https://brevis.sh/en/docs/ingestion-sinks/ · brevis.sh docs (en)*

---

Six destinations, and the list is deliberately explicit — a connector that is
*planned* and a connector that *runs* are different things to somebody choosing
this.

| sink | state | what a write is | proven against |
|---|---|---|---|
| `pubsub` | **runs** | one publish per batch, attributes and ordering key from the event's own fields | the emulator |
| `postgres` | **runs** | `COPY FROM STDIN`, or a staged `INSERT` in one transaction | a real Postgres |
| `mysql` | **runs** | multi-row `INSERT` in a transaction, or `INSERT IGNORE` | a real MySQL |
| `files` | **runs** | NDJSON objects in a directory, `gs://` or `s3://` | the filesystem, and S3 |
| `bigquery` | **runs** | rows loaded, or staged and `MERGE`d on `ingestion_id` | **config only — no emulator exists** |
| `redshift` | **runs** | the batch to S3, then `COPY`, then `MERGE` | **config only — no emulator exists** |

**Two of the six are unproven end to end**, and the table says so rather than
letting the word "runs" carry a claim nobody checked. BigQuery and Redshift have
no local emulator, so what is tested is the config, the refusals and the driver
underneath — which the SDK's own suite already covers. The first production use
of either is the real proof, and it should be a low-stakes stream.

An unknown `type:` is **refused at startup**, naming what *this binary* carries.
A gateway that starts on a sink it does not have is one that drops events for a
reason nobody can see.

`gs://` and `s3://` are not separate connectors: `files` reads all three path
shapes, so the dead letter is a folder on a laptop and a bucket in production
with no change here.

## How each one writes

`write` is **required and never defaulted** on the four destinations that write
to a table, because the two modes are genuinely different and only the table's
owner knows which it is:

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | multi-row `INSERT` per block | `INSERT IGNORE` |
| **bigquery** | rows loaded (through GCS above the inline limit) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | `COPY` from S3 | `COPY` to a staging table, then `MERGE` |

`merge` means **the same thing in all four**: the idempotent insert, and the
**first delivery wins**. A redelivery is ignored, not applied — which is right
for an event, which happened once and does not change, and wrong for a row
carrying a mutable state.

Postgres and MySQL **require a `UNIQUE` index on `ingestion_id`** for `merge`,
and refuse without one rather than silently appending:

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

Without the index `ON CONFLICT` has nothing to match, and every redelivery would
duplicate into a table whose owner asked for the opposite. The refusal is not a
lost batch: it is retried, then buried in the dead letter with the reason and
the `CREATE INDEX` on the record — nobody loses events while somebody creates
the index.

**`upsert` is refused by name.** `ON CONFLICT DO UPDATE`, where the *last*
delivery wins, is a real mode that is **not implemented**. Accepting the word
while behaving like `merge` is exactly the failure that field exists to prevent,
so the config says so instead.

## Each one with what is true only of it

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` names the **environment variable** holding the connection string,
never the string: a DSN carries a password and that file is in git. It is the
same split `secrets:` makes in a workflow.

`iam_role` is a role and the driver **will not accept a key**: a key in a
`COPY`'s URL ends up in the cluster's query log, which plenty of people read.

## No column declaration

The gateway sends no column list. A pipeline declares its own because its rows
have one shape it controls; a gateway's batch is whatever *N* clients posted in
one flush window, and the shapes are allowed to differ.

The driver resolves the list from the **table itself**, intersected with what
the batch carries, **before** touching the server. A field the table does not
have is refused with the message that fixes it, instead of failing mid-`COPY`
with `column "x" of relation "y" does not exist`; a field one event omits is
written as `NULL`.

## The import list is the selection

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

The Go linker prunes what nothing references, so a binary that never imports the
BigQuery driver does not carry BigQuery — or Arrow, or the Storage Write API.
That is how `database/sql` has always worked.

It is worth what it sounds like:

| build | packages | binary |
|---|---|---|
| all six sinks, both object stores, both metastores | 884 | 55.0 MB |
| `postgres` + local `files` | 233 | **10.2 MB** |

Object stores are a **second** registry, because a scheme is not a destination:
`files` is one sink that writes to a directory, to `gs://` and to `s3://`, and a
build that only writes locally should not carry the AWS SDK to do it.

### Two published images

| image | carries | pull (amd64) |
|---|---|---|
| `areteacademy/brevis-gateway:0.8.0` | six sinks, S3, GCS, Redis, memcached | 17.9 MB |
| `areteacademy/brevis-gateway:0.8.0-slim` | `postgres`, local `files`, `auto_table` | **4.9 MB** |

Both from one build of one tree, so the two tags are always the same commit.
There is no `latest-slim` — `latest` is already a tag nobody should deploy.

**Want a different pair?** `cmd/gateway-slim` is fifteen lines and the import
block is the whole of its configuration. Copy it, change the imports, build.
Anybody who wants a hook is compiling their own binary already, so choosing the
sinks costs them nothing more.

### The refusal says which build you are holding

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

A fixed list would send that person looking for a config mistake they did not
make. The same holds for an object store: an `s3://` dead letter in a binary
with no S3 backend is refused **at startup**, naming the scheme, rather than on
the first batch it would have to bury.

## One route, N tables, nothing declared

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table` **routes and does not write**: `into` is what writes, and it
carries no `table` — the table comes from each event. An `into` with a `table`
is refused at startup, because a fixed table under a router would win silently
and every event would land in it.

### The envelope

The body is an **envelope**: control fields at the top, the record inside
`data`.

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| field | | what it is |
|---|---|---|
| `table_name` | **required** | where this lands |
| `data` | **required** | the record, and only it |
| `unique_key` | defaults to `id` | which field of `data` identifies the record — **required under `merge`** |
| `operation` | defaults to `INSERT` | `INSERT`, `UPDATE` or `DELETE` |
| `description` | optional | **accepted and not yet used** — see below |

`table_name` sitting beside `total` and `customer` was a field of the
**transport** pretending to be a field of the **record**. Separating them is how
every CDC format is shaped — and it is what makes reserving a prefix possible,
because now there is one place only the producer writes.

### The key belongs to `merge`, not to the envelope

`unique_key` is optional, and whether it is **required** depends on the write
mode — because the two modes want different things:

| | |
|---|---|
| `write: merge` | **required.** The mode exists to keep one row per record, and `brevis_record_key` is what "per record" means: the `qualify` that resolves the current version partitions by it. A merge table full of rows naming no record is a table nobody can resolve. |
| `write: append` | **optional.** An append table is a log, and a log entry need not be *about* a record: an audit line, a webhook, a metric sample. Demanding an `id` there would make producers invent one, which is worse than `NULL` because it looks real. |

Under `append` the column is created nullable and a keyless row carries `NULL`
— not `""`, which is a different fact. And `brevis_ingestion_id` still exists:
with no key, **the content is the key**, so the same document twice is the same
id.

**Naming a field that is not in `data` is an error in both modes.** "You did not
say" and "you said something that is not there" are different mistakes, and only
the first one passes:

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` is read and today goes nowhere.** It is in the contract for the
console's ingestion page, which has not been written. It is said here because a
field accepted in silence is a field somebody believes is being stored.

### Seven fixed columns, plus whatever the record carries

| column | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | the identity, and the merge key |
| `brevis_record_key` | `STRING` | `data[unique_key]` — which **record** this is about; `NULL` under `append` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE` or `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | arrival, on **our** clock — the partition column |
| `brevis_loaded_at` | `TIMESTAMP` | the write, stamped by the **destination** |
| `brevis_stream` | `STRING` | which route wrote it |
| `brevis_gateway` | `STRING` | which deployment |
| `brevis_received_bytes` | `INT64` | how large the event **arrived**, envelope included |

**`brevis_` is reserved.** A `data` carrying any key with that prefix is refused
**per event** — otherwise a producer forges a control field, and a forged
`brevis_received_at` is worse than none because it looks real.

**`brevis_loaded_at` is a database `DEFAULT`**, not a value the gateway sends.
The gateway knows the **dispatch** time, the destination knows the **write**
time, and the gap between the two columns is the real end-to-end latency, per
row, with nothing instrumented.

**`brevis_received_at` is the partition column, never a client's clock**: a
producer's can be wrong or absent, and a partition column the client controls is
a client that can write into 2035. An unpartitioned landing table is a query
bill that only grows. Clustering is by `brevis_record_key`, because reading one
record's history is what anybody does with a table like this.

One declaration serves every destination, because the SDK's DDL generator
translates each type: `STRING` becomes `TEXT` on Postgres, `LONGTEXT` on MySQL
and `STRING` on BigQuery; `JSON` becomes `JSONB`, `JSON` and `JSON`.

### Volume without infrastructure to measure volume

`brevis_received_bytes` is **free**: the gateway already counts these bytes at
decode to drive `buffer.flush.size`, and until now the number died there.
Measuring the record alone would cost a `json.Marshal` per event — 2.0 µs
against the 3.1 µs the parse already spends, **67% more on the hot path** — to
refine a number whose job is trend and attribution.

It answers what the metrics **structurally cannot**. Every `brevis_gateway_*`
series is per *stream* and carries no `table` label, deliberately: with
`auto_table` one route becomes N tables and that label is the producer's to
choose. So "which table is growing, and since when" had no answer anywhere.

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

Without the column the same question means scanning the whole JSON column —
`sum(length(to_json_string(data)))` — roughly **fifty times the bytes scanned**,
every time somebody asks. The column pays for itself on the first query.

**It is ingress, not storage.** The destination keeps the row typed and
compressed: 400 bytes of JSON may be 80 on disk. Summing this gives what
*arrived*, never what is billed for keeping it.

And **the producer cannot write it**. The gateway stamps the value after the
hook and overwrites whatever the envelope carried — a client who sent the field
would be reporting their own volume, and summing it would stop meaning
anything.

### The volume series are opt-in

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**Off by default**, and the default is the point: `table` is a label the
*producer* chooses. Everything else in this file is labelled by what an operator
wrote in the YAML, so the series count is known before the process starts.
Nobody should find a metrics bill because they upgraded.

A value nobody can parse reads as **off**, never as a failure: this is
observability, and a typo here must not be the reason an ingestion endpoint does
not start.

The column is there either way. The metric buys *now*; the column buys *since
when* and *by whom*.

### Two shapes, one contract

`shape` decides what the **record** contributes to the row. The producer's
contract is the same either way — the envelope never changes. What changes is
what the table looks like, which is the operator's decision and not the
producer's.

```yaml
shape: document   # the default
shape: columns
```

With **`document`**, `data` goes whole into one `JSON` column:

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

There is never any DDL after the create. A new field is a new key: no
schema-change quota, no write-stream reopen, no race between replicas — and it
is queryable the day it arrives. It is where the market landed: Fivetran,
Airbyte and Snowpipe all land into a raw layer that cannot fail and model it
downstream.

With **`columns`**, each field of the record gets a column of its own:

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**Scalars become `STRING`, objects and arrays become `JSON`, and there is no
inference anywhere.** `150` arrived as a number and became the string `150`.
That is a choice rather than a limitation: a field that arrives whole today and
fractional tomorrow would change a column's type with nobody writing anything,
and the row that no longer fits goes to the dead letter. Typing one is the
**promotion** path — written in the YAML and reviewed in a diff.

A field name has to match BigQuery's rule, the narrowest of the four. Postgres
would accept almost anything quoted, and that is the trap: the table is created
there and it breaks the day somebody points a stream at BigQuery.

### `UPDATE` and `DELETE` are recorded, not applied

`brevis_operation` is a column. A landing table is **history**: an `UPDATE`
applied in place loses the previous version, and the day you want that version
is the day something broke. Resolving "the current version of each record" is
the downstream model's job:

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

Which is what Debezium, Fivetran and Airbyte do — and it means CDC costs
**nothing** in the write path: no upsert mode, no lock, no delete.

### The identity has no clock in it

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

There is no `occurred_at` in the formula, and that is deliberate: arrival became
**our** responsibility, and our clock is `time.Now()` — different on every
delivery. An identity with it inside would make every redelivery a new event,
and `merge` would stop absorbing anything.

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

The caveat is the documented one: two genuinely distinct events with
byte-identical `data` collapse into one. For CDC that is **correct** — two
identical updates to one record with the same values are the same fact.

The fingerprint is over the document in **canonical** form — keys sorted at
every level, arrays left in order, everything quoted. Go randomises map
iteration, so a naive hash would differ between two deliveries of the same
event, which is the one thing it exists to prevent.

### The table grows a column on its own

A `data` carrying a field the table does not have makes the column appear.
**Additive, and only additive**: nothing is dropped, nothing is narrowed.

A batch that loses the race to `ALTER` fails, and the pipe's ordinary retry
resolves it — a batch waiting for DDL and a batch waiting for a worker are the
same thing, so there is no second buffer. Across replicas the metastore holds a
one-second *debounce* per table and per shape, so that ten replicas seeing the
same new field do not become ten `ALTER`s against BigQuery's quota of five
metadata operations per table per ten seconds.

**A debounce and not a lock**: nothing is released and no lease is renewed.
Whoever loses is being *retried*, not blocked, and a replica that dies holding
one costs the others a second. It is what the market does — Delta Lake and
Iceberg commit optimistically and retry, Kafka Connect gets serialisation from
partition ordering, Fivetran keeps one writer per table.

### The name is the attack surface

`auto_table` turns a string in a payload into DDL, so every field under
`naming` is a refusal:

- **`pattern`** defaults to `^[a-z][a-z0-9_]{2,48}$` — the intersection of what
  Postgres, MySQL, BigQuery and Redshift accept unquoted. A name that passes
  needs no quoting anywhere and cannot carry an injection.
- **`allow`** narrows further, by prefix.
- **`max_new_per_hour`** (default 20) bounds **creations** in a rolling hour,
  never writes: a table that already exists is never rate limited. Under
  `metastore: memory` the budget lives in the process and four replicas admit
  four times this; under `redis` or `memcached` they share one counter.

A name outside the rules is refused **per event**, in the response, and every
well-formed event in the same request still lands:

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

Per event and not per batch, deliberately. Refusing it at write time would fail
the whole batch — one malformed event from one producer burying the events of
every other producer in the same flush window, none of them told.

**`auto_table` is refused on an endpoint with no `listen.auth`**, including
under `BREVIS_ENV=local`. A producer that can name a table can create one, so it
needs authentication even where an ordinary stream would not. A NetworkPolicy
does not cover it: that limits who reaches the port, not which table name they
ask for.

### The metastore is a cache

It exists so a per-event write is not a per-event lookup, and it holds three
things: which tables exist, the DDL *debounce*, and the counter behind
`max_new_per_hour`.

It is a cache and **not** a source of truth. The destination settles whether a
table exists, and *N* replicas racing to create one is the normal case:
`AlreadyExists` is success, and this only reduces the race. The TTL is how long
it may be **wrong**.

| backend | when |
|---|---|
| `memory` | the default, needs nothing. With **one** replica it is the right answer |
| `redis` | `addr_from` required. A `SETNX` is the claim, an `INCR` is the counter |
| `memcached` | `addr_from` required. An `Add` is the claim, and expiry is **second**-granular |

With several replicas, `memory` gives each its own: the debounce debounces
nothing and `max_new_per_hour` bounds a process. That is the only reason the
other two exist.

**A metastore that is down must not take a write down with it.** A `Get` that
errors is a miss, a claim that errors behaves as won, a counter that errors
relaxes the limit. A cache that can stop the ingestion is worse than no cache.

`addr_from` names the **environment variable** holding the address, never the
address: it carries a password often enough.

### BigQuery has a flush floor

**BigQuery allows 1,500 load jobs per table per day.** The default one-second
window is 86,400 — 57× the quota, gone in about twenty-five minutes. It is not a
problem for Pub/Sub, and the number would arrive here *from* a Pub/Sub config.

So a stream that writes to BigQuery, directly or through a router, is **refused
at load** with a window under 60s. The real answer is the Storage Write API,
which has not been written; until it is, the config refuses a window it cannot
honour rather than letting the first deploy find out.

`auto_table` cannot route into **Redshift**: that driver creates no tables, so
every new name would fail on the load. Refused by name, at startup.

## What does not exist yet

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` and `rabbitmq` are
planned and nothing has been written. (`redis` and `memcached` exist as a
**metastore**, which is a different thing: a cache of what is known about the
tables, never a destination.) The table at the top of this page says
the state of each, and it is updated when the state changes — not before.
