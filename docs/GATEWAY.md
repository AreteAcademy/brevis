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
| `areteacademy/brevis-gateway:X` | all six sinks, S3 and GCS | 48.6 MB |
| `areteacademy/brevis-gateway:X-slim` | `postgres`, local `files` | **12.4 MB** |

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
