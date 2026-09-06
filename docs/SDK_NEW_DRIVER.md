# SDK — how to add a driver

**Valid for** `sdk/v0.52.0` · **Updated on** 2026-09-06

Five drivers were added this way: Files, Postgres, MySQL and Redshift, plus
BigQuery before them. Read `sdk/from/postgres/postgres.go` and
`sdk/to/postgres/postgres.go` alongside this document — they are the shortest
complete pair.

For the map, see [`SDK_ARCHITECTURE.md`](SDK_ARCHITECTURE.md); for the decisions
this walkthrough assumes, [`SDK_DECISIONS.md`](SDK_DECISIONS.md).

---

## 1. The skeleton

A driver is a type carrying the fields **only it** has, plus one method:

```go
// sdk/from/postgres/postgres.go
package postgres

type Query struct {
    DSN       string
    SQL       string
    Args      []any
    FetchSize int
    Timeout   time.Duration
    Conn      *pgx.Conn
}

func (q Query) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error)
func (q Query) Describe() string
```

```go
// sdk/to/postgres/postgres.go
package postgres

type Table struct {
    DSN  string
    Name string
    Conn *pgx.Conn
}

func (t Table) Write(ctx context.Context, envelopes []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error)
func (t Table) Describe() string
```

Two packages with the same name is on purpose, and the consumer imports them as
`frompg` and `topg` when both are needed: the read side has `SQL`, the write side
has `Name`, and neither carries the other's field.

---

## 2. The nine rules

### 2.1 A driver with a dependency lives in its own package

`from` and `to` hold the drivers that need only the standard library. Any one
with a vendor SDK behind it — BigQuery, Postgres, MySQL, Redshift — goes into a
package of its own: `to/bigquery`, `to/postgres`.

Sharing a package with an expensive driver has the same effect as the root
importing it. It happened in `v0.20.0`: `to.BigQuery` and `to.Files` together
made writing a file compile Google in, 461 packages where it should have been
195.

### 2.2 The root must not import your package

If `sdk` starts importing `from/postgres`, every consumer compiles `pgx` — and
the property phase 0 bought dies. `examples/consumer/pruning_test.go` says so,
and `.github/scripts/pruning-check.sh` runs it in CI. **Do not fix the test; fix
the import.**

Add your case there, with the control: whoever imports your package *has* to get
your dependency, or the test would pass with a driver that carries nothing.

### 2.3 The cloud backend is a value too

If `from.Files` imported S3 and GCS, reading a local CSV would compile both. That
is why `core.Store` is passed in rather than chosen inside the driver, and lives
in `store/s3` and `store/gcs`.

It holds for any driver that speaks to more than one backend: **what varies
becomes a value, and the value lives in its own package.**

### 2.4 Streaming, always

`Read` returns an `iter.Seq2` that **produces on demand**. A driver that
materializes the whole source before returning puts a 5 GB export in memory.

Write the test that fails if you buffer — HTTP's is
`extract.TestBodyStreamsFully`, and it exists because that regression already
happened: a `cancelAttempt()` called too early truncated the body, and no test
saw it.

### 2.5 No type inference

The SDK does not guess a column's type. On BigQuery, `v0.16.0` resolves that by
delegating the inference to BigQuery itself — it loads into a throwaway table
with autodetect, reads the schema and overrides only the SDK's two columns.

**Postgres, MySQL and Redshift have no such service.** So for them:

> The table has to exist, or you pass the DDL in `CreateSQL`.

A `CreateTable: true` with no `CreateSQL` on a SQL destination is an **error
naming the limitation**, and the message lists the columns the batch carries so
the DDL comes out of one reading. It is not a gap to be filled later with
inference: it is the decision.

### 2.6 An option the driver does not support is an error, not silence

`Dedup` on a file destination, `ClusterBy` on Postgres, a `RateLimiter` on a
disk source: an error naming the option and the driver.

A field accepted and ignored is the defect this SDK has found most often in
itself.

### 2.7 Generated SQL is a pure function

Build the SQL outside the method that needs a connection:

```go
func InsertSQL(target, source string, columns []string) string
func core.Reconcile(dest, incoming []string, target string) ([]string, error)
```

It is not style. BigQuery's `MERGE` spent **three versions** on `INSERT ROW`,
which matches columns by **position**, with a comment claiming it matched by
name — and no unit test had ever seen the generated string, because it was born
inside a method that held a client.

And **backticks or quotes on every identifier**: `full`, `range` and `comment`
are reserved and show up in a real consumer's columns.

### 2.8 Reconciliation is asymmetric

When matching the record against the destination, use the same rule
`core.Reconcile` already uses:

| situation | what to do |
|---|---|
| a field in the record the destination does not have | **error** naming the field |
| a column in the destination the record does not carry | carry on, it stays NULL |
| incompatible types under the same name | **error** naming the column and both types |

Discarding data in silence is the worst way to fail: it vanishes with no signal.
A column that stays NULL is legitimate in a landing table.

`Reconcile` lives in `internal/core` and serves all four SQL destinations.

### 2.9 Alter nothing and delete nothing

The principle written in `load.prepareTable`'s godoc holds for every driver: a
loader that can `ALTER` can erase history. A divergence is an error, not a
migration. And do not create an index — check that the unique index on
`ingestion_id` exists and refuse naming it when it does not.

---

## 3. The two metadata columns, per dialect

| | `ingestion_id` | `ingestion_loaded_at` |
|---|---|---|
| BigQuery | `STRING NOT NULL` | `TIMESTAMP NOT NULL` |
| Postgres | `TEXT NOT NULL` | `TIMESTAMPTZ NOT NULL` |
| MySQL | `VARCHAR(36) NOT NULL` | `DATETIME(6) NOT NULL` |
| Redshift | `VARCHAR(36) NOT NULL` | `TIMESTAMPTZ NOT NULL` |

**The driver does not add them.** Since `v0.24.0` they are transformers — the
consumer places `sdk.IngestionID(...)` and `sdk.IngestionLoadedAt()` in the
chain, and the row that reaches `Write` already carries them. There is no
`WriteOptions.Metadata` and no `AutoID`: a destination that stamped a column
after the chain would be writing something the declaration check never saw.

What the driver does is honour `WriteOptions.Columns`, which names them.

## 4. Dedup, per dialect

| destination | how |
|---|---|
| BigQuery | `MERGE ... WHEN NOT MATCHED THEN INSERT (cols) VALUES (...)` |
| Postgres | staging + `INSERT ... ON CONFLICT (ingestion_id) DO NOTHING` |
| MySQL | `INSERT IGNORE`, with a unique index on `ingestion_id` |
| Redshift | `COPY` into staging + `MERGE`, with the columns **named** |
| Files | not supported — an error saying so |

All of them match on `ingestion_id`, so all of them need it declared in
`Columns`, and the SQL ones need the unique index to exist. They **require** the
index and never create one.

---

## 5. Type mapping for the SQL drivers

The record becomes JSON, so every type needs a **written** choice:

| SQL | Go | JSON | why |
|---|---|---|---|
| `NUMERIC` / `DECIMAL` | `string` | string | a `float64` loses precision on money |
| `TIMESTAMPTZ` | `time.Time` | RFC 3339 | |
| `DATE` | `time.Time` | `YYYY-MM-DD` | with no invented time |
| `BYTEA` / `BLOB` | `[]byte` | base64 | `encoding/json` already does it |
| `JSON` / `JSONB` | `json.RawMessage` | nested | do not reserialize |
| `UUID` | `string` | string | |
| `NULL` | `nil` | `null` | |
| a PG array | `[]any` | array | |

A written table, tested line by line, is not inference: it is a reviewable
decision. On Postgres the conversion comes from the column's declared **OID**;
on MySQL from `Rows.ColumnTypes()`, because `database/sql` returns `[]byte` for
nearly everything when read into an `any` — without it every `INT` becomes a
string of bytes.

The two directions are in `sdk/from/postgres/types.go` and
`sdk/to/postgres/types.go`, with their MySQL counterparts alongside.

---

## 6. Tests

Bring the containers up with `docker-compose.drivers.yml` and gate on an
environment variable, the way the BigQuery tests already are:

| service | serves | variable |
|---|---|---|
| `postgres:17-alpine` | `postgres.Query`, `postgres.Table` | `BREVIS_IT_PG_DSN` |
| `mysql:8` | `mysql.Query`, `mysql.Table` | `BREVIS_IT_MYSQL_DSN` |
| `minio/minio` | `s3://` for `Files` and Redshift's staging | `BREVIS_IT_S3_ENDPOINT` |

GCS has no good emulator; `gs://` runs against the real bucket the BigQuery
suite already uses.

**Per driver, at minimum:**

1. a test proving a row **actually** goes in or comes out. The in-memory ones
   prove the bytes we assembled, not what the server accepts — and it was the
   first run of the integration tests that found four defects at once;
2. the type mapping, line by line from §5's table, with `NULL`, `NUMERIC` and
   `JSONB`;
3. one that **fails** if the driver buffers instead of streaming;
4. the generated SQL asserted as a pure function, with no client;
5. the pruning case in `examples/consumer/pruning_test.go`, with the control;
6. a runnable example under `examples/`, that works the first time. It was an
   example that did not run which found the hole in `03-basic-load`.

**Check that the test bites.** Revert the fix and confirm it fails, before
calling it done. This is the rule that has found the most defects in this
project.

---

## 7. Done checklist

- [ ] `Read`/`Write` and `Describe` implemented
- [ ] a driver with a dependency is in its own package
- [ ] the root still does not import the package — the pruning test with the
      control, **the complete pipeline from both sides included**
- [ ] a backend that varies lives in its own package, passed as a value
- [ ] streaming proven by a test that would fail without it
- [ ] types mapped by a written table, with a test per row
- [ ] `CreateTable` with no inference: an existing table or `CreateSQL`
- [ ] an unsupported option is an error naming the option and the driver
- [ ] generated SQL is pure and tested; identifiers quoted
- [ ] asymmetric `Reconcile`
- [ ] no `ALTER`, no `DROP`, no index creation
- [ ] integration against a container, gated on an environment variable
- [ ] a runnable example that works the first time
- [ ] `CHANGELOG` with the migration diff written out
- [ ] `go test ./... -race` green, `golangci-lint run ./...` clean
- [ ] `cmd/brevis-sdk` still compiles (its own module; the pin moves after the tag)

---

## 8. The final question

Take a fetcher written by somebody who has never seen the SDK and answer by
reading only its `main.go`:

> Where does it come from, what comes out in each column, where does it go, and
> how big is the binary?

The architecture already delivers the first three. The fourth is the pruning, and
it is what says whether the SDK is ready for somebody who does not use BigQuery.
