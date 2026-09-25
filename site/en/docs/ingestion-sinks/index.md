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

## What does not exist yet

`sqlite`, `redis`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` and `rabbitmq`
are planned and nothing has been written. The table at the top of this page says
the state of each, and it is updated when the state changes — not before.
