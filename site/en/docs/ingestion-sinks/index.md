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
| all six sinks, both object stores | 864 | 48.9 MB |
| `postgres` + local `files` | 232 | **10.0 MB** |

Object stores are a **second** registry, because a scheme is not a destination:
`files` is one sink that writes to a directory, to `gs://` and to `s3://`, and a
build that only writes locally should not carry the AWS SDK to do it.

### Two published images

| image | carries | pull |
|---|---|---|
| `areteacademy/brevis-gateway:0.3.2` | six sinks, S3 and GCS | 16.1 MB |
| `areteacademy/brevis-gateway:0.3.2-slim` | `postgres`, local `files` | **4.8 MB** |

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
  table_from: table_name
  naming: {pattern: '^[a-z][a-z0-9_]{2,48}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: memory, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

A producer `POST`s `{"table_name": "app_orders", ...}` and the table is created
if it is absent. `auto_table` **routes and does not write**: `into` is what
writes, and it carries no `table` — the table comes from each event.

### Four columns, always

| column | | |
|---|---|---|
| `ingestion_id` | `STRING` | the identity, and the merge key |
| `ingested_at` | `TIMESTAMP` | arrival, on **our** clock — the partition column |
| `occurred_at` | `TIMESTAMP` | the producer's, when they send it; `NULL` when not |
| `data` | `JSON` | the event whole |

**One JSON column, not a column per field**, and this is the decision the rest
rests on. A new field is a new key: no DDL, no schema-change quota, no
write-stream reopen, no race between replicas — and it is queryable the day it
arrives. A column per field buys types nobody declared and pays for them with
all four.

It is also where the market landed: Fivetran, Airbyte and Snowpipe all land
into a raw layer that cannot fail and model it downstream. And it is what keeps
`table_from` cheap — every table is the same shape, so creating one is a
template rather than a decision.

One declaration serves every destination, because the SDK's DDL generator turns
`TypeJSON` into `JSON` on BigQuery, `JSONB` on Postgres, `JSON` on MySQL and
`SUPER` on Redshift.

**`ingested_at` and not `occurred_at` as the partition column**: a producer's
clock can be wrong or absent, and a partition column the client controls is a
client that can write into 2035. An unpartitioned landing table is a query bill
that only grows.

### The identity, with nothing declared

```
ingestion_id = uuid5(auto_table | <table> | <idempotency_key>  | <occurred_at>)
             = uuid5(auto_table | <table> | sha256(canonical)  | <occurred_at>)
```

A declared stream names four fields its owner knows. A producer sending
`{table_name, data}` names none, so the key is theirs when they send one and a
fingerprint of the document when they do not.

**Send `idempotency_key`.** It means *these two requests are the same business
fact*, which only the producer knows. The fingerprint means *these two requests
have identical bytes* — a good approximation and a worse promise, because two
genuinely distinct events with byte-identical data collapse into one. Both beat
a random id, under which a retried `POST` is a second row forever.

The fingerprint is over the document in **canonical** form — keys sorted at
every level, arrays left in order, everything quoted. Go randomises map
iteration, so a naive hash would differ between two deliveries of the same
event, which is the one thing it exists to prevent.

### The name is the attack surface

`auto_table` turns a string in a payload into DDL, so every field under
`naming` is a refusal:

- **`pattern`** defaults to `^[a-z][a-z0-9_]{2,48}$` — the intersection of what
  Postgres, MySQL, BigQuery and Redshift accept unquoted. A name that passes
  needs no quoting anywhere and cannot carry an injection.
- **`allow`** narrows further, by prefix.
- **`max_new_per_hour`** (default 20) bounds **creations** in a rolling hour,
  never writes: a table that already exists is never rate limited. **Per
  replica** — the budget lives in the process, so four replicas admit four
  times this.

A name outside the rules is refused **per event**, in the response, and every
well-formed event in the same request still lands:

```json
{"accepted": 1, "rejected": ["event 1: the table name \"x\";DROP TABLE y;--\" does not match ^[a-z][a-z0-9_]{2,48}$"]}
```

Per event and not per batch, deliberately. Refusing it at write time would fail
the whole batch — one malformed event from one producer burying the events of
every other producer in the same flush window, none of them told.

**`auto_table` is refused on an endpoint with no `listen.auth`.** A producer
that can name a table can create one, so it needs authentication even where an
ordinary stream would not. A NetworkPolicy does not cover it: that limits who
reaches the port, not which table name they ask for.

### The metastore is a cache

It exists so a per-event write is not a per-event lookup, and it caches **both**
answers — "this table does not exist" is what saves a round trip on the hot path
of a new producer retrying.

It is a cache and **not** a source of truth. The destination settles whether a
table exists, and *N* replicas racing to create one is the normal case:
`AlreadyExists` is success, and this only reduces the race. The TTL is how long
it may be **wrong**.

**`memory` is the only backend, and that is the design.** A gateway that cannot
start without Redis is a gateway with a new hard dependency for a cache. `redis`
and `memcached` are refused **by name**, because somebody writing `redis`
believes their replicas share a cache — and accepting the word while caching per
process would make `max_new_per_hour` *N* times what they set.

### BigQuery has a flush floor

**BigQuery allows 1,500 load jobs per table per day.** The default one-second
window is 86,400 — 57× the quota, gone in about twenty-five minutes. It is not a
problem for Pub/Sub, and the number would arrive here *from* a Pub/Sub config.

So a stream that writes to BigQuery, directly or through a router, is **refused
at load** with a window under 60s. The real answer is the Storage Write API,
which has not been written; until it is, the config refuses a window it cannot
honour rather than letting the first deploy find out.

`auto_table` cannot route into **Redshift**: that driver creates no tables, so
every new name would fail on the load. Refused by name.

## What does not exist yet

`sqlite`, `redis`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` and `rabbitmq`
are planned and nothing has been written. The table at the top of this page says
the state of each, and it is updated when the state changes — not before.
