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

## [0.11.0] — 2026-09-26

Volume, without infrastructure to measure volume.

### Added: an eighth fixed column, `brevis_received_bytes`

How large the event arrived, envelope included, on every row of every table.

**It is free.** The gateway has counted these bytes at decode since `0.10.0` to
drive `buffer.flush.size`, and the number died there. Measuring the record
alone would cost a `json.Marshal` per event — measured at 2.0µs against the
3.1µs the parse already spends, a **67% increase on the hot path**, or about
40% of one core at the rates the buffer sustains — to refine a number whose job
is trend and attribution.

**It answers what the metrics structurally cannot.** Every `brevis_gateway_*`
series is per STREAM and carries no `table` label, deliberately: with
`auto_table` one route becomes N tables and that label is the producer's to
choose. So "which table is growing, and since when" had no answer anywhere:

```sql
select date(brevis_received_at) day, count(*), sum(brevis_received_bytes)/1e9 gb
from app_orders group by 1 order by 1 desc
```

Without it, the same question means scanning the whole JSON column — roughly
fifty times the bytes, every time somebody asks. The column pays for itself on
the first query.

**It is INGRESS and not storage.** The destination keeps the row typed and
compressed, so 400 bytes of JSON may be 80 on disk. Summing this gives what
arrived, never what is billed for keeping it.

**A producer cannot write it.** The value is stamped after the hook, through a
new `Measurer` seam, and it OVERWRITES whatever the envelope carried — the same
rule the `brevis_` prefix enforces inside `data`, applied to the one control
field the pipe writes from outside. A forged `brevis_received_bytes: 1` lands
as the number the gateway measured.

Nullable, and absent rather than `0` when nothing measured it: a zero SUMS, and
a column whose zeros mean "nobody looked" lies in aggregate — which is the only
way this one is ever read.

### Added: per-table volume series, behind `BREVIS_INGESTION_METRICS`

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**Off unless `BREVIS_INGESTION_METRICS` is true**, and the default is the
point. Everything else in this gateway is labelled by what an OPERATOR wrote in
a YAML file, so the series count is known before the process starts. `table` is
chosen by the PRODUCER, and `naming` bounds that rather than making it small.
Nobody should find a metrics bill because they upgraded.

A value nobody can parse reads as off, never as a failure to start: this is
observability, and a typo must not be the reason an ingestion endpoint does not
come up.

The two answer different questions and both are the same number. The metric
buys *now*; the column buys *since when* and *by whom*.

### The `Measurer` seam

Optional and structural, like `Admitter`, and for the same reason: most sinks
must NOT get this. A stream landing in a topic or in a table somebody declared
would find a field in its payload nothing asked for — a new key for every
subscriber, or a column the table does not have and the load refuses. Only a
sink that owns its table's shape can carry it.

It returns the table the volume is attributed to, so the pipe can own the
metrics while the sink owns the routing. A name `naming` refuses is attributed
to nothing rather than becoming a series named by a string a producer chose.

---

## [0.10.0] — 2026-09-26

Every ceiling in the buffer was a count, and a count cannot see bytes.

### Added: `buffer.flush.size`, a third trigger

```yaml
buffer:
  flush:
    every: 1s
    records: 500
    size: 8MiB     # whichever is crossed FIRST
```

`records: 500` is 1 MB of 2 KB events and 1 GB of 2 MB ones — the same number
in the file. A producer who starts sending the whole document instead of its id
changes what the gateway holds by three orders of magnitude, and nothing in the
config could have said otherwise.

Measured on what **arrived**: not what the event weighs in memory — a
`map[string]any` is several times its JSON — and not after a hook that inflates
it. An approximation, and the right one. It is free at decode (ndjson knows each
line's length; the array format goes through `json.RawMessage` so every
element's extent is known without a second pass), it travels with the payload,
and the thing it guards against is a record that grew tenfold. Exact would mean
a `json.Marshal` per event, which today happens only when `oversize` is
configured.

### Added: `buffer.max_bytes`, the ceiling that stops an OOM

`flush.size` shapes what **leaves**; this bounds what is **held**, and with
`queue: 64` those are very different numbers. Past it the answer is the same
`503` the record ceiling gives, and it is safe to retry for the same reason.

It is refused below `listen.max_body` — a buffer that cannot admit one request
would answer 503 to everything, forever — and below `flush.size`, where a batch
could never fill.

### Added: `brevis_gateway_flushes_total{stream,trigger}`

`time`, `records` or `size`. Its own counter rather than a label on
`batches_total`, which already carries `sink` and `outcome`: a third dimension
there would multiply the series for a question that belongs to the buffer and
not to the delivery.

**The one worth an alert is `trigger="size"` on a BigQuery stream.** That
destination allows 1,500 load jobs per table per day, which is why
`flush.every` has a 60-second floor — and the floor governs the TIMER. A size
that fires every few seconds walks straight past it and spends the same daily
quota. Nothing refuses it at load, because the arrival rate is not knowable
there; this makes it visible instead.

A handoff the pool refuses is not counted: nothing flushed, and counting there
would report one per attempt on a stream whose sink is behind — turning the
metric into noise exactly when it matters.

### And what this is not

It is not the answer to BigQuery throughput. With load jobs the ceiling is the
quota — one per 57 seconds per table — so the best available strategy is to
FILL each of the 1,500, which argues for bigger batches and longer windows. The
thing that removes the ceiling is the Storage Write API, which is still
unwritten. This is memory safety and per-job hygiene.

### Changed: the BigQuery sink stages past 64 MiB, whatever the row count says

Carries `sdk v0.68.0`, where the same blind spot was one layer down:
`ThresholdForGCS` is a row count, so 5,000 records of 2 MB went down the inline
path and 10 GB was marshalled into memory. `InlineLimitBytes` is its sibling,
and the gateway sets it — it always has a staging bucket, which is exactly the
reason the SDK leaves it off by default for callers who may not.

Not a config field, deliberately. It is not a tuning knob; it is the point past
which holding a batch in memory stops being reasonable. `buffer.flush.size` is
where an operator shapes the batch.

---

## [0.9.1] — 2026-09-26

`auto_table` into BigQuery created the table and loaded nothing. The bug was in
the SDK; this is the version that carries the fix.

A consumer found it and diagnosed it to the line. Every table came out right --
`PARTITION BY DATE(brevis_received_at)`, `CLUSTER BY brevis_record_key`, every
column, `brevis_record_key` nullable as `0.9.0` promises for `append` -- and
every load job was refused with a 400:

```
Expects   interval(type:day,field:brevis_received_at) clustering(brevis_record_key)
but input                                             clustering(brevis_record_key)
```

The job declared clustering and said nothing about partitioning, and BigQuery
compares the pair. The SDK's `applyLayout` decided "somebody else creates this
table" by asking whether the caller's columns include one of the SDK's OWN
metadata names -- and `auto_table` v2 renamed those to `brevis_*` in `0.6.0`.
So the rename broke a proxy three modules away, and the symptom was a table
that existed and stayed empty.

There was no config to work around it: neither the YAML nor `Sink` exposes
partitioning or clustering, which is deliberate -- every `auto_table` table has
the same layout, so it is a template and not a decision.

**Nothing in the gateway changed.** `sdk v0.67.0` is the fix; this bumps the
dependency and republishes the image so the tag carries it.

---

## [0.9.0] — 2026-09-25

Two poison batches, and the second one was a rule that did not fit `append`.

**Breaking**: `unique_key` is no longer required under `write: append`, and the
refusal a `merge` stream gives for a missing one is a different message in a
different place. Minor and not patch, which is what 0.x does with a break --
`0.8.0` and `0.6.0` were the same call.

### Fixed: `auto_table` admitted a record whose field cannot be a column

Four events in one request, one of them carrying `meu-campo`:

```
{"accepted":4,"rejected":null}
```

Two seconds later the whole batch was in the dead letter, the table was never
created, and the reason named event 2. The other three were well formed, came
from other producers, and had all been answered `202`.

`Admit` existed for exactly this and covered only half of it: the envelope and
the table name went behind it, the FIELD names did not, because they are the
shape's business and the shape only ran at write time. Under `shape: columns` a
name that fails BigQuery's rule — a hyphen, a leading digit, a space — reached
`Write` and failed the group.

The shape now validates per event, before anything is buffered:

```
{"accepted":3,"rejected":["event 2: the field \"meu-campo\" cannot be a column
name: it has to match ^[A-Za-z_][A-Za-z0-9_]{0,127}$ …"]}
```

Three rows land, nothing is buried, and the producer learns it in the response
at the moment they can still fix it. `shape: document` refuses nothing here, and
that is not an oversight: the record becomes one JSON column, so a key is a key.

**It scales with the batch.** The load test ran batches of roughly 8,700
events; one bad field name would have buried eight thousand from every producer
in the same flush window.

Two tests pin it, and the second is the general form: everything the write path
refuses about a record, `Admit` has to refuse first.

### Changed: `unique_key` is required by `merge` and optional under `append`

It used to be required of every event, in both modes, and the two modes do not
want the same thing:

| | |
|---|---|
| `merge` | **required.** The mode keeps one row per record, and the `qualify` that resolves the current version partitions by `brevis_record_key`. A merge table full of rows naming no record is a table nobody can resolve. |
| `append` | **optional.** A log entry need not be *about* a record: an audit line, a webhook, a metric sample. Demanding an `id` there makes producers invent one, which is worse than NULL because it looks real. |

`brevis_record_key` is created nullable under `append` — a NOT NULL column
would have had the database refuse the row at write time, which is the same
poison batch through another door. A keyless row carries NULL and not `""`,
because the resolving query would gather every `""` into one partition.

The id survives without a key: `Envelope.IngestionID` refuses an empty
SourceKey, so the fingerprint fills the slot. Same document twice, same id.

**Naming a field `data` does not carry is still an error in both modes.** "You
did not say" and "you said something that is not there" are different mistakes,
and only the first one passes.

## [0.8.0] — 2026-09-25

The drain had one budget and three owners, and the first could eat it all.

The gateway answers `202` before anything is written: what is in a buffer at
`SIGTERM` is delivered by the drain, and a drain that runs out of time loses
events a producer was told had been accepted. With Pub/Sub the flush window is
a second; with BigQuery the floor is sixty and the recommended window is three
hundred — so the same drain went from protecting one second of accepted events
to protecting five minutes of them.

### Fixed: a slow request could spend the drain's entire budget

`http.Shutdown`, `Server.Close` and the metrics shutdown shared one
thirty-second context, in that order. A single slow reader — a large `ndjson`
body, a client on a bad connection — spent the drain's budget before the drain
began, and in the worst case `Close` received an already-expired context: the
last batch still went out through `close`'s own fallback, and **everything
already queued was abandoned**.

Three budgets now: five seconds to stop accepting, the whole drain budget for
the drain, five seconds for the metrics.

### Changed: the drain budget follows the flush windows

It was thirty seconds, fixed, chosen when every window was one second. It is now
the longest flush window, floored at thirty seconds, and `shutdown.drain`
overrides it:

```yaml
shutdown:
  drain: 90s
```

One window and not two: the drain waits for nothing — it takes the pending batch
immediately and the workers deliver in parallel, so draining is strictly faster
than the filling was. Doubling it would only lengthen how long a genuinely stuck
pod takes to die, which is a rollout everybody waits on.

It is a heuristic and says so in the code. What actually bounds a drain is the
queue depth and the cost of one delivery, and neither is knowable there: a load
job takes seconds, a file takes none. An installation that has measured its own
should declare `shutdown.drain`.

### Changed: a failed drain exits non-zero, and says how many

**This is the breaking one.** A drain that lost events used to log and return 0,
which to Kubernetes, to a supervisor and to CI is indistinguishable from a clean
stop. It exits `1` now, and the message carries the count:

```
drain incomplete: stream "tables": the drain did not finish and 412 accepted
event(s) were not delivered: context deadline exceeded
```

`Server.Close` also joins every stream's failure instead of returning the first:
reading "stream A lost 12" while stream B silently lost 4,000 is worse than
reading both. `Server.Pending()` reports the same number programmatically.

### Added: the gateway states the grace period it needs

```
drain budget 5m0s; set terminationGracePeriodSeconds >= 310
listening on :8080
```

It cannot read its own manifest, and Kubernetes defaults
`terminationGracePeriodSeconds` to thirty — which equalled the old fixed budget
exactly, so there was no margin and a `SIGKILL` could arrive while the drain was
still inside its own deadline. Printing the requirement is the cheapest thing
that stops the two drifting apart in two repositories with nothing connecting
them.

**Check your manifest against this line.** A gateway whose streams flush at 60s
asks for 70; at 300s it asks for 310.

## [0.7.1] — 2026-09-25

Three things two replicas found that no unit test could.

### Fixed: the second replica refused to start

```
stream "tables": another replica is altering this table for this shape; retrying
```

The startup probe went through the same path a batch does, so it took a claim —
and the second gateway to start lost it and **would not come up**. Losing a
debounce is a batch to retry; it can never be a reason a gateway does not
start. The probe builds the destination directly now, and takes no claim and no
slot in the creation counter, which it had no business spending either.

### Fixed: the loser of a claim was buried instead of retrying through

The claim window was three seconds and the pipe spends its four attempts in
roughly two, so a loser never got back in — its batch went to the dead letter.
**A debounce that can bury a batch is not a debounce.**

One second now, and the number is bounded from both sides: long enough to cover
one `ALTER` — which is all it has to cover, since a loser proceeding after the
winner finishes plans no change at all — and short enough that the pipe's
jittered backoff outlasts it.

### Fixed: the cache was never invalidated

`forget` was written, documented as the rule that keeps the cache safe, and
called by nothing — the linter said so after a refactor removed its only
caller. A failed write now drops what was believed about that table, in both
the local map and the shared store: **if the cache says the column is there and
the write fails, the write is right.**

### And a flaky test of my own

`TestTheCounterWindowRolls` used a two-second TTL checked at 1.4s, and
memcached's expiry has **second granularity** — an item stored for 2s can be
gone at 1.x. It failed about one run in three. Four seconds now, with a second
of margin on each side, and it still fails against the mutation.

---

## [0.7.0] — 2026-09-25

### The metastore has backends: `redis` and `memcached`

```yaml
metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
```

`memory` stays the default and needs nothing. It is also **right with one
replica and wrong with several**, which is the whole reason the other two
exist: with `memory` each replica has its own, so the debounce debounces
nothing and `naming.max_new_per_hour` bounds a *process*. Ten replicas meeting
one new field would issue ten `ALTER`s against BigQuery's five metadata
operations per table per ten seconds.

Four primitives — `Get`, `Put`, `Claim`, `Incr` — and the set is the
intersection of what all three do **atomically**. Anything richer would work on
one and be emulated badly on the others.

### `Claim` is a debounce, not a lock

Nothing is released and no lease is renewed: a replica that dies holding one
costs three seconds to the batches behind it, and they are batches being
retried rather than workers blocked. Losing it returns an error and the pipe
retries; waiting would hold a worker, and there are four.

Three seconds is bounded from both sides — long enough to collapse a burst into
one attempt, short enough that a loser gets through on its own retries, since
the pipe backs off 500ms, 1s, 2s.

It is what the market does: Delta Lake and Iceberg commit optimistically and
retry, Kafka Connect gets serialisation free from partition ordering, Fivetran
has one writer per table. Nobody takes a lock.

### A metastore that is down cannot fail a write

A `Get` that errors is a miss, a `Claim` that errors behaves as won, an `Incr`
that errors relaxes the rate limit. A cache that can stop ingestion is worse
than no cache — and the destination is the source of truth anyway.

### Tested against the real thing, and the three behave the same

One table of behaviour run against `memory`, Redis and memcached, plus a
20-goroutine race proving exactly one `Claim` wins. Four mutations fail,
including the quiet one: extending the counter's TTL on every increment makes a
busy key immortal, so `max_new_per_hour` stops being a rate and becomes a
lifetime total. The first version of that test **passed** against the bug — its
sleeps outlasted both behaviours instead of separating them.

### What it costs

The full image goes to 55 MB. The slim one stays at **10.2 MB**: the backends
are packages, like the sinks, so a binary that never imports one does not carry
it — and the weight test now watches for them.

---

## [0.6.0] — 2026-09-25

**Breaking**: `auto_table`'s body is an envelope now. The old flat shape is
refused by name, with what to change in the message.

### The envelope

```json
{
  "table_name": "app_orders",
  "unique_key": "id",
  "operation":  "INSERT",
  "data": { "id": "A-3", "total": 150, "customer": {"id": 7, "uf": "SP"} }
}
```

Control fields at the top, the record inside `data`. `table_name` sitting beside
`total` and `customer` was a field of the TRANSPORT pretending to be a field of
the RECORD, and separating them is how every CDC format is shaped.

`table_from` is gone: the envelope names the table in `table_name`, always.

### Seven `brevis_*` columns

`brevis_ingestion_id`, `brevis_record_key`, `brevis_operation`,
`brevis_received_at`, `brevis_loaded_at`, `brevis_stream`, `brevis_gateway` —
plus whatever the record carries.

`brevis_loaded_at` is a database `DEFAULT` and not a value the gateway sends:
the gateway knows the **dispatch** time and the destination knows the **write**
time, so the gap between the two columns is the real end-to-end latency, per
row, with nothing instrumented.

`brevis_received_at` is the partition column and never a client's clock — a
partition column the client controls is a client that can write into 2035.

**`brevis_` is reserved.** A `data` carrying any key with it is refused per
event: otherwise a producer forges a control field, and a forged
`brevis_received_at` is worse than none because it looks real.

Naming the identity column `brevis_ingestion_id` needed `sdk/v0.66.0`
(`WriteOptions.DedupKey`): every driver matched on `ingestion_id` BY NAME, so
the first version created tables with the right columns into which every merge
refused. Zero rows, and the integration test caught it.

### The identity has no clock in it

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

`occurred_at` stopped being the client's responsibility, and that removed the
only clock the old formula could have used — ours is `time.Now()`, different on
every delivery, so every retry would have been a new event.

Same record, same content, twice → same id. Same record, changed → a different
id, so an UPDATE is a second row rather than a no-op.

### UPDATE and DELETE are recorded, not applied

`brevis_operation` is a column. A landing table is history; resolving the
current version is the downstream model's job. Which is what Debezium, Fivetran
and Airbyte do — and it means CDC costs **nothing** in the write path: no upsert
mode, no lock, no delete.

### Two shapes, one contract

`shape: document` puts the record whole in one JSON column — no DDL, ever.
`shape: columns` gives each field a column: **scalar → STRING, object or array
→ JSON, no inference anywhere**.

A field name must match BigQuery's rule, the narrowest of the four. Postgres
would accept almost anything quoted, and that is the trap: the table is created
there and it breaks the day somebody points a stream at BigQuery.

### The table grows a column on its own

Additive, and only additive. A batch that loses the race to `ALTER` fails and
the pipe's **ordinary retry** resolves it — a batch waiting for DDL and a batch
waiting for a worker are the same thing, so there is no second buffer.

Not solved yet: the metadata quota with many replicas. Ten detecting one field
is ten `ALTER`s against BigQuery's five per table per ten seconds. That needs a
shared debounce, which needs a shared metastore.

---

## [0.5.0] — 2026-09-25

Closes `auto_table` against its plan.

### The metastore is configurable, and only `memory` exists

```yaml
metastore: {type: memory, ttl: 60s}
```

The TTL was fixed at a minute; a file can name its own now. `redis` and
`memcached` are refused **by name** rather than left out of the list, because
somebody writing `redis` believes their replicas share a cache — and accepting
the word while caching per process would make `naming.max_new_per_hour` *N*
times what they set, which is exactly the number they wrote it down to bound.

That memory is the design and not a limitation is the plan's own reasoning: a
gateway that cannot start without Redis is a gateway with a new hard dependency
for a cache.

### `max_new_per_hour` is per replica, and the docs now say so

The budget lives in the process. Four replicas admit four times the number.
Nothing was wrong with it; nothing said it either, and a limit somebody sets to
bound a deployment while it bounds a process is a surprise worth preventing in
prose rather than in an incident.

### A table `auto_table` creates says where its rows came from

```
Written by auto_table/app_orders via the Brevis SDK since 2026-09-25.
```

Free: the BigQuery driver already writes a description from the envelope's
provider and entity, and the router now sets them. BigQuery only — Postgres and
MySQL take a `COMMENT`, which the SDK's DDL generator does not write yet.

**A `description` in the payload does not survive**, and the reason is the one
that killed a producer-supplied `schema`: the table is created inside a batch
holding *N* events for it, so "only on creation" means "whichever event happened
to be first". That is worse than last-write-wins, not better. The plan asked for
the payload version; this is the same intent with the non-determinism removed.

### Two wires that compiled whether or not they were connected

The config's TTL reaching the cache, and the provider and entity reaching the
envelope. Both were right, and cutting either left every test passing — the
package tests exercise the pieces directly, and the integration test reads rows
out of Postgres, which has no table description to check. Both have a test now,
and all three mutations fail.

---

## [0.4.2] — 2026-09-25

### Fixed: a table `auto_table` created could not be merged into

0.4.0 shipped `write: merge` as the natural default for an event stream, and it
could not work. `DedupMerge` needs a unique index on `ingestion_id` and
Postgres and MySQL refuse without one — so the router created a table with the
right four columns and every load into it refused. Two tables, zero rows, and
the reason in the dead letter.

The table now carries the constraint, and it follows the write mode in BOTH
directions, because getting it wrong breaks the table one way or the other:

| | |
|---|---|
| `merge` | **needs** it, or every load refuses |
| `append` | must **not** have it, or the second delivery is rejected as a duplicate |

BigQuery is left out of both: it has no unique constraints and its `MERGE`
needs none.

Needs `sdk/v0.65.0`, which added `Column.Unique`. That is not a contradiction
of the drivers' refusal to create indexes — the objection is about an index
added to a table people are already using, and this is a constraint on a table
that is empty and that nobody has yet.

Found by running the published image, and the end-to-end test that now covers
it checks the row count AND the index count, in both modes.

---

## [0.4.1] — 2026-09-25

### Fixed: the slim image could not run `auto_table`

`cmd/gateway-slim` registered `postgres` and `files` and not the router, so the
slim image refused the feature 0.4.0 shipped:

```
sink type "auto_table" is not one this binary carries (it has: files, postgres)
```

The refusal was right and the build was wrong. `auto_table` is pure Go with no
client of its own — it routes into the Postgres sink already there — and it
costs **0.04 MB and one package**. Leaving it out put the cheapest way to land
arbitrary events behind the 49 MB image.

Found by running the published image, not by reading the code: every test
passed, because the tests build their own registry.

---

## [0.4.0] — 2026-09-25

### `auto_table`: one route, N tables, nothing declared

A producer POSTs `{"table_name": "app_orders", ...}` and the table is created
if it is absent. `auto_table` routes; `into` writes.

**Four columns, always** — `ingestion_id`, `ingested_at`, `occurred_at` and
`data` as JSON. One JSON column and not a column per field, which is the
decision the rest rests on: a new field is a new key, so there is no DDL, no
schema-change quota, no write-stream reopen and no race between replicas, and
it is queryable the day it arrives. One declaration serves every destination,
because the SDK's DDL generator already turns TypeJSON into JSON on BigQuery,
JSONB on Postgres, JSON on MySQL and SUPER on Redshift.

Partitioned on `ingested_at` and never on `occurred_at`: a partition column a
client controls is a client that can write into 2035.

**The identity survives having nothing declared.** A frozen UUID v5 over
`auto_table|<table>|<idempotency_key or sha256(canonical)>|<occurred_at>`. The
fingerprint is over the document in canonical form, pinned by a test that hashes
the same document 200 times — Go randomises map iteration, so a naive hash would
differ between two deliveries of the same event, which is the one thing it
exists to prevent. A second test proves two DIFFERENT documents cannot collide,
and it is the one that matters: `fmt`'s `%v` sorts map keys and looks stable
enough to use, but `{"a":"b:1"}` and `{"a:b":"1"}` both render `map[a:b:1]`.

**The name is the attack surface**, so `naming` is three refusals: a pattern
defaulting to the intersection of what all four databases accept unquoted, an
optional prefix allowlist, and a cap on CREATIONS per rolling hour — never on
writes, so a settled producer is never slowed by an unsettled one.

**`auto_table` is refused on an endpoint with no auth.** A producer that can
name a table can create one.

### Admitter: the poison batch, cured where it starts

A sink may now refuse ONE event before it is buffered. Optional, satisfied by
structural typing, and it is what keeps a bad table name from burying a flush
window: the producer gets the reason in the response and everybody else's events
land.

The first version of `auto_table` refused at write time and did bury the batch.
The integration test caught it — the good table was never created.

### BigQuery has a flush floor

1,500 load jobs per table per day against a default one-second window is 86,400
— 57x, gone in about twenty-five minutes. A BigQuery stream with a window under
60s is refused at load, directly or through a router. It caught the example
shipped in 0.3.x, which used `every: 5s`.

The real answer is the Storage Write API, and it is not written yet.

### Also

- `Build` carries the registry and a `Target`, so a routing driver can build
  what it routes into — through the same registry, which is what makes a binary
  that did not compile in BigQuery unable to route into it.
- The `into` of a router is resolved at STARTUP, not on the first event.
- The example test grew two more holes it did not have: the sink a router routes
  into, and a driver whose package name and YAML type differ.

---

## [0.3.2] — 2026-09-25

### Fixed: the module did not compile for anybody outside this repository

`gateway/v0.3.1` published an image that worked and a module that did not
build. Its `go.mod` required `sdk v0.63.0` while its code used `sdk.Store`,
which landed in `v0.64.0`:

```
$ go get github.com/AreteAcademy/brevis/gateway@v0.3.1
registry.go:126:43: undefined: sdk.Store
```

A `replace` directive in a dependency's go.mod is **ignored** by the main
module, so what a consumer compiles against is whatever `require` names. The
local `replace … => ../sdk` hid it completely, and every gate in this
repository was green: nothing built the module the way the outside world gets
it.

The gateway is a module somebody imports and not only an image they pull — a
hook means a binary of their own — so `gateway-consumer-check.sh` now copies
the module, drops the replace, and builds. It runs in CI and again in the
release workflow before the tag becomes an image, because a release is
immutable and an image that works while its module does not cannot be told
apart from the outside.

Found while writing a consumer's own binary, not by reading the code.

---

## [0.3.1] — 2026-09-24

### Fixed: the response did not say an event had been archived

An oversized event with no reduction hook is archived whole and dropped from
the stream, which is right. The answer was `{"accepted":0,"rejected":null}`,
which is not: a caller cannot tell that from "nothing happened", and the
difference is the whole point of the claim check.

It now carries `"archived": 1` — and only when there is one, so the field
appearing means something happened rather than being a zero everybody scrolls
past.

Found by running the published image against a real oversized payload rather
than by reading the code. The tests all passed the whole time; none of them
looked at the response body.

---

## [0.3.0] — 2026-09-24

Everything a service already in production needs before it can be replaced by
this one.

### Metrics

Prometheus exposition on **its own address**, never on the ingest mux — that
port is public by design, and a `/metrics` on it would publish every stream
name, path and destination. Naming one address for both is refused at load.

Eleven instruments. Three of them matter more than the rest and nobody asks for
them until after the first incident: `saturated_total` is the only number that
says a client was told to back off, and `buffer_records` against
`buffer.max_records` is the only one that predicts it.

`reason` is a closed enum and never the error text: an error string carries a
table name, a column, sometimes a row, and one malformed client would mint a new
series per request. The text stays in the dead letter, on the record.

Hand-written, zero dependencies — the OTel SDK costs 40 packages here and
`prometheus/client_golang` 43, for eleven instruments whose format has not
changed in a decade. The slim build went from 10.0 MB to 10.1. Both mistakes the
engine's own exposition names have a test: buckets are running totals, and a
quote inside a label ends the series early and corrupts every line after it.

### The claim check, for events too big

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

The event is archived **whole** and a reduced version continues, carrying
`_oversize_archive` and `_oversize_bytes` back to it. A flat `413` loses the
event, and an oversized payload is usually the most interesting one somebody
has.

Stamped rather than implicit: agreeing out of band that the id is also the
object's name works until somebody changes the prefix. Without a reduction hook
the event is archived and dropped — not lost, and counted.

### `stamp_loaded_at`

Writes `ingestion_loaded_at` with the SDK's column name and the SDK's format, so
a row this gateway lands and a row a pipeline lands are the same shape. Opt-in.

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
