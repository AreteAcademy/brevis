# SDK — what each driver supports

**Valid for** `sdk/v0.58.0` · **Updated on** 2026-09-10

What works with what, and what happens when it does not. A driver that
**ignores** an option does not appear here: in this SDK it refuses, naming the
option and the driver.

---

## 1. Sources

| | `from.HTTP` | `from.Files` | `postgres.Query` | `mysql.Query` |
|---|---|---|---|---|
| formats | JSON, NDJSON, CSV, XML | JSON, NDJSON, CSV, XML | rows | rows |
| streaming | yes | yes | yes | yes |
| `Preview` | yes | yes | yes | yes |
| `Stats` (pages, attempts, bytes) | yes | pages = files; no attempts | one page | one page |
| pagination | Link header, cursor, offset | not applicable | in your SQL, by key | in your SQL, by key |
| retry with backoff | yes (429, 5xx, network) | no | no | no |
| `RateLimiter` | yes | no | no | no |
| `Records` | yes | not applicable: a file is not a response | not applicable | not applicable |
| compression | whatever the server negotiates | `.gz` by extension | not applicable | not applicable |
| deterministic order | the pagination's order | **yes, guaranteed** | your `ORDER BY` | your `ORDER BY` |

### `from.HTTP`, point by point

| | |
|---|---|
| `Method`, `Body`, `Header` | POST and PUT with their own body and headers |
| `Timeout` / `TotalTimeout` | per attempt / across the whole walk |
| `RetryConfig` | attempts, exponential backoff, jitter, `Retry-After` |
| `FollowLinks` | RFC 8288, `rel="next"` |
| `CursorKey` | a cursor in the body, sent back as a parameter of the same name |
| `PageKey` + `FirstPage` | a page number, advancing one at a time |
| `OffsetKey` + `PageSize` | an offset in rows, advanced on every page |
| `MaxPages` | the walk's ceiling; a repeated cursor also stops it |
| `MoreKey` | a boolean in the response saying whether there is a next page |
| `DataKey` | unwraps the array; **refused alongside `Records`** |
| `Header["Cookie"]` | seeds the jar; `Set-Cookie` refreshes by name on the next page |
| `Auth.Value` + `Apply` | where the secret comes from and how it enters the request |
| `Auth.Login` | trades secrets for a token, using the SDK's own client |
| `Auth.TTL` | caches the login in memory, under a lock; never touches disk |
| `Auth.Refresh` | a GET before the first page; the jar absorbs the `Set-Cookie` |
| `Auth.Refresh.Store` | keeps the rotated credential between runs |
| `Auth.Refresh.ExpiresAt` + `WarnAfter` | warns before the credential expires — in the log **and** in `Stats.CredentialExpiry` |

**Two pagination strategies together is an error**, not a precedence rule — the
loser would be a written field that does nothing.

**Every 2xx** reaches `Records`, `204` and `206` included. A non-2xx is an error
with the status and the body, with a retry where that makes sense.

### `from.Files`, point by point

| | |
|---|---|
| `Path` | `./x/*.csv`, `/var/data/`, `s3://b/p/*.ndjson`, `gs://b/p/` |
| `Store` | `nil` is disk; `s3.New(...)`, `gcs.New(...)` |
| `NoHeader` | a CSV with no header, keyed by `field_N` |

An empty directory is a result, not a failure. A `.gz` that is not gzip fails
naming the file.

### `from/postgres` and `from/mysql`, point by point

| | |
|---|---|
| `postgres.Query{DSN, SQL, Args}` | reads a SELECT, one row per record, **as a stream** |
| `mysql.Query{DSN, SQL, Args}` | the parameters are `?`; also a stream |
| types | from the declared **OID** on Postgres and from `information_schema.data_type` on MySQL — not from the Go type, which does not tell `DATE` from `TIMESTAMPTZ` |
| paginate by KEY | `WHERE id > $1 ORDER BY id LIMIT $2`. There is no `Offset` field, on purpose: OFFSET on a large table is O(n²) |

---

## 2. The matrix, and what it promises

**This table is a test**, not a piece of prose: `sdk/capabilities_test.go`
checks every row against the code, and fails if a driver accepts an option
without implementing it.

For each combination there are only two acceptable answers. The third — "accepts
and ignores" — is the class of defect this project has found most often in
itself.

| destination | `Dedup` | `CreateTable` | `Preview` | `Metadata` |
|---|---|---|---|---|
| `bigquery.Table` | `MERGE` | **yes**, BigQuery infers the types | yes | transformers |
| `postgres.Table` | `ON CONFLICT DO NOTHING` | **does not exist** — the table has to exist | yes | transformers |
| `mysql.Table` | `INSERT IGNORE` | **does not exist** | yes | transformers |
| `redshift.Table` | `MERGE … WHEN NOT MATCHED` | **does not exist** | yes | transformers |
| `to.Files` | **refused**, naming `Dedup` | **does not exist** | yes | transformers |
| `pubsub.Topic` | **refused**, naming `Dedup` | **does not exist** — the topic exists already | yes | **attributes** |

**`pubsub.Topic` refuses three, which is more than any other destination.** It
is the first one here that is neither a table nor a directory, and half of what
`WriteOptions` asks has no meaning on a topic:

| | |
|---|---|
| `Schema` | refused. A `Schema` exists so a destination can CREATE its table; a topic exists already, and a Schema here would be a declaration nothing reads — worse than an error, because the author believes it is enforced. Declare `Columns` instead: they still check the row the chain composed |
| `Dedup` | refused. There is no key to match on and no row to replace. Pub/Sub is at-least-once by design, and every message carries `ingestion_id` so the subscriber can be idempotent |
| `PartitionBy` | refused. A partition is a table's idea; the nearest thing is `OrderingKey`, which groups messages that must ARRIVE in order rather than rows that live together |

**And it is the only destination where a failure is partial.** Every other one
here is all-or-nothing at the batch level. A publish is not: 48,000 messages
that fail at 31,000 have *delivered* 31,000, so `RowsLoaded` reports what
actually went on the error path too, and the error says a re-run is a
re-delivery. The metadata goes in message **attributes** rather than through the
transformers, so a subscription filters on it without parsing the body and the
payload keeps the producer's own schema.

**Why only BigQuery creates a table.** It has a service that infers the types
from the data, and `v0.16.0` uses exactly that, overriding only the SDK's two
columns. Postgres, MySQL and Redshift have no equivalent, and deducing
`NUMERIC(18,2)` from an `encoding/json` number would be guessing — the one thing
this SDK decided not to do. On all three, the error lists the batch's columns so
the DDL comes out of one reading.

**A directory has no unique key and no schema**, so `to.Files` refuses `Dedup`
rather than offering a flag that does nothing.

## Measured throughput

Numbers from `-bench Load…`, against the containers in
`docker-compose.drivers.yml`, 10 thousand rows of 5 columns per run, **on the Go
1.27 toolchain**. They are for comparing the strategies, not a production
promise — the machine, the network and the row's width change everything.

And so does the toolchain: measuring the same code on Go 1.25 and 1.27,
Redshift's `EncodeNDJSON` went from 13 to 40,017 allocations because of an
escape-analysis change, with nothing in the code changing. An allocation count is
a property of the code **plus the compiler**.

| destination | strategy | rows/s | allocations per row |
|---|---|---|---|
| `postgres.Table` | `COPY FROM STDIN` | ~434,000 | ~19 |
| `mysql.Table` | multi-row `INSERT` | ~137,000 | ~1 |

The throughput difference is the difference between `COPY` and `INSERT`, and not
one of care: MySQL has no reliable `COPY`. The allocation difference is the
inverse — pgx's path types every value, and `database/sql`'s passes `any` along.

---

## 3. Destinations

| | `bigquery.Table` | `to.Files` | `postgres.Table` | `mysql.Table` | `redshift.Table` |
|---|---|---|---|---|---|
| `Columns` (the declaration) | yes | yes | yes | yes | yes |
| the two ingestion columns | from `Transform`; `NOT NULL` when declared | from `Transform` | from `Transform` | from `Transform` | from `Transform` |
| `Dedup: DedupMerge` | yes, through `MERGE` | **refused** | `ON CONFLICT` | `INSERT IGNORE` | `MERGE` |
| creating the destination | `CreateTable`, `CreateSQL` | creates the directory | **no** | **no** | **no** |
| partitioning | by day on `ingestion_loaded_at` | `PartitionBy` becomes `field=value/` | not applicable | not applicable | not applicable |
| clustering | `ClusterBy` | not applicable | not applicable | not applicable | not applicable |
| compression | from the staged format | `Compress` (gzip) | not applicable | not applicable | not applicable |
| atomic write | a BigQuery job | temp+rename, or one PUT | one transaction | one transaction | `COPY` + `MERGE` |
| written formats | NDJSON | NDJSON, CSV | rows | rows | NDJSON via S3 |

### The refused combinations, and why

| combination | what happens |
|---|---|
| `DedupMerge` with no `ingestion_id` in `Columns` | **error** — the merge matches on that column |
| `DedupMerge` + `RequirePartitionFilter` | **error** — the merge scans every partition and cannot be scoped |
| partition options with no `ingestion_loaded_at` in `Columns` | **error** — that is the column it partitions on |
| `Dedup` on `to.Files` | **error** — a directory has no key to match on |
| Parquet on `to.Files` | **error** — it would drag Arrow in for somebody who only wanted a file |
| `Records` + `DataKey` | **error** — both say where the records are |
| a cloud `Path` with no `Store`, or a `Store` for another scheme | **error** naming both sides |
| `DedupMerge` with no unique index on Postgres/MySQL | **error** — `ON CONFLICT`/`INSERT IGNORE` would have nothing to match, and every run would insert duplicates |
| an access key instead of `IAMRole` on Redshift | **error** — the key would end up in the cluster's query log |

### `bigquery.Table`, point by point

| | |
|---|---|
| `Project`, `Dataset` | fall back to the environment; see the `Env*` constants |
| `Name` | no default |
| `StagingBucket`, `StagingPrefix` | above `InlineLimit`, it goes through GCS |
| `InlineLimit` | zero uses 5000 |
| `CreateTable` | three-state: `nil` lets the engine decide |
| `CreateSQL` | your DDL, checked against the table it produced |
| `ClusterBy` | columns checked against the rows before submitting |
| `PartitionExpiration` | zero keeps them forever |
| `KeepStagedFile` | the zero value deletes |

The SDK **never** alters a table that exists. A divergence is an error, not a
migration.

### `to/postgres` and `to/mysql`, point by point

| | |
|---|---|
| `postgres.Table{DSN, Name}` | loads through `COPY FROM STDIN` |
| `mysql.Table{DSN, Name, BatchSize}` | a multi-row `INSERT` in a transaction |
| `BatchSize` | exists here and **not** on Postgres: there is no `COPY`, and a large packet runs into `max_allowed_packet` |
| a unique index | **required**, never created — a loader that creates an index can lock a production table |
| `CreateTable` | **does not exist**: the table has to exist, and the error lists the batch's columns |

### `to/redshift`, point by point

| | |
|---|---|
| `redshift.Table{DSN, Name, Staging, IAMRole, Store}` | `COPY` from S3; **there is no inline path** |
| `IAMRole` | a role ARN; **an access key is refused** — it would end up in the cluster's query log |
| `Dedup: DedupMerge` | a temp `LIKE` the destination, `COPY`, `MERGE … WHEN NOT MATCHED`, `DROP` |
| `KeepStagedFile` | leaves the object in S3 for inspection |

**Partial verification, and it is here rather than in a footnote:** there is no
Redshift image. What is tested without a cluster is the SQL generation as a pure
function, the staging write and the order of the commands; what is **not** tested
is that a real cluster accepts that SQL.

---

## 4. What is proven, and how

Every row above has a test. **649 tests** run in the module, and this is where
they are:

| package | tests |
|---|---|
| `sdk` (the facade) | 164 |
| `extract` | 105 |
| `load` | 76 |
| `internal/core` | 59 |
| `from` | 46 |
| `to/redshift` | 39 |
| `to/postgres` | 32 |
| `pycompat` | 30 |
| `from/postgres` | 21 |
| `to`, `from/mysql` | 17 each |
| `to/mysql` | 15 |
| `internal/jsontext` | 13 |
| `to/bigquery` | 9 |
| `store/gcs` | 5 |
| `internal/checkpoint` | 1 |

What is proven against the real service, and not only in memory: `from.HTTP`
against `httptest`, the file drivers against the filesystem, S3 against MinIO in
a container, Postgres and MySQL against their containers, GCS and BigQuery
against real ones.

```bash
# the disk and HTTP ones always run
go test ./...

# the cloud ones ask for the variables
docker compose -f docker-compose.drivers.yml up -d minio postgres mysql
BREVIS_IT_S3_ENDPOINT=http://localhost:9000 \
BREVIS_IT_PROJECT=my-project BREVIS_IT_DATASET=brevis_it \
BREVIS_IT_BUCKET=my-bucket \
  go test ./... -run Integration
```

**Without the variables they skip**, and the normal suite stays offline. That is
on purpose: a test that needs a credential and fails without one becomes a test
everybody learns to ignore.

---

## 5. What is not true yet

Said here so it is not discovered in production:

- **Parquet is written** by no destination.
- **`from.Files` is not incremental**: it reads what the path names. A window is
  made in the path itself (`day=2026-09-04/`), with the `RunContext`.
- **The SDK infers the types of the client's columns** when creating a BigQuery
  table — by delegating to BigQuery's own autodetect. Its own two columns are
  declared; yours are not. See §13 of [`SDK_DECISIONS.md`](SDK_DECISIONS.md).
- **`to.Files` does not deduplicate**, so a re-run writes the batch again, into a
  new file. The layer below is what resolves that.
- **Redshift is not exercised against a cluster**, only against its SQL. See §3.
