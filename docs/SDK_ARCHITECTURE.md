# SDK — the architecture as it is

**Valid for** `sdk/v0.52.0` · **Updated on** 2026-09-06

This is the map. For *why* each piece is the way it is, see
[`SDK_DECISIONS.md`](SDK_DECISIONS.md); for *how to add a driver*, see
[`SDK_NEW_DRIVER.md`](SDK_NEW_DRIVER.md).

---

## 1. The four questions

A fetcher answers four things, and each has a place:

```go
sdk.Run(sdk.Pipeline{
    // 1. Where it comes from, and what a response means.
    Source: sdk.Source{
        From: from.HTTP{
            URL:     "https://api.open-meteo.com/v1/forecast?...",
            Timeout: 15 * time.Second,
            Records: func(r sdk.Response) ([]any, error) { ... },
        },
        Preview: 5,
    },

    // 2. What row it builds.
    Transform: []sdk.Transformer{
        sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
        sdk.Rename(map[string]string{"temperature_2m": "temperature_celsius"}),
        sdk.Compute("provider", ...), sdk.Compute("entity", ...),
        sdk.ComputeText("source_key", sdk.Key("latitude", "longitude", "time")),
        sdk.IngestionID("provider", "entity", "source_key", "time"),
        sdk.IngestionLoadedAt(),
    },

    // 3. Where it goes, 4. with which columns.
    Target: sdk.Target{
        To:      bigquery.Table{Dataset: "bronze", Table: "hourly_temperatures"},
        Columns: []string{"ingestion_id", "ingestion_loaded_at", "time", "temperature_celsius"},
        Dedup: sdk.DedupMerge,
    },
})
```

The same thing in two calls, which is what the tests use:

```go
data, err := sdk.Extract(ctx, source)
data = sdk.Transform(data, fns...)
res, err := sdk.Load(ctx, data, target)
```

---

## 2. The package map

```
sdk/
├── *.go                  the facade: Pipeline, Run, Extract, Load, Transform,
│                         Reduce, Stage, Source, Target, Metadata, Result,
│                         RunContext, Checkpoint
├── internal/core/        the contract: Envelope, Reader, Writer, ReadOptions,
│                         WriteOptions, Response, Reading, Stats, Dedup,
│                         LoadConfig, Origin, Reject
├── internal/checkpoint/  the depot that lets a second attempt skip the extract
├── internal/jsontext/    a leaf package: JSON string escaping, Python's way
├── from/                 the sources with no vendor dependency: HTTP, Files
├── from/postgres/        the Postgres source, in a package of its own
├── from/mysql/           the MySQL source, in a package of its own
├── to/                   the destinations with no vendor dependency: Files
├── to/bigquery/          the BigQuery destination, in a package of its own
├── to/postgres/          the Postgres destination
├── to/mysql/             the MySQL destination
├── to/redshift/          the Redshift destination
├── pycompat/             Python-compatible rendering, for a migration
├── store/                the object-storage backends: s3, gcs
├── extract/              the HTTP implementation: retry, pagination, decoders,
│                         preview
└── load/                 the BigQuery implementation: staging, MERGE, typed
                          table creation
```

**The root imports neither `from`, nor `to`, nor `extract`, nor `load`.** That is
the rule everything else rests on, and it is measured — see §6.

| who depends on whom | |
|---|---|
| `sdk` → `internal/core` | only |
| `from/*` → `internal/core`, `extract` | |
| `to/*` → `internal/core`, `load` | |
| `store/*` → nothing from the SDK | only the cloud's client |
| `extract`, `load` → `internal/core` | |

No arrow points back at `sdk`. A driver cannot import the facade, and that is
why `RunContext` and configuration resolution live in `core`.

---

## 3. The two interfaces

```go
type Reader interface {
    Read(ctx context.Context, opt ReadOptions) (iter.Seq2[Envelope, error], error)
    Describe() string
}

type Writer interface {
    Write(ctx context.Context, records []Envelope, opt WriteOptions) (*LoadResult, error)
    Describe() string
}
```

`Describe()` is what shows up in the log and in the error message —
`"bronze.orders"`, `"http://api.example.com/v1/events"` — already free of any
secret in a query string.

### What crosses every driver

| in `ReadOptions` | |
|---|---|
| `Preview`, `PreviewBytes`, `PreviewWriter` | the `head()`-style table |
| `Stats` | pages, attempts, bytes read |
| `Run` | the engine's run context |

| in `WriteOptions` | |
|---|---|
| `Columns` | the destination's declaration |
| `Dedup` | the deduplication asked for |
| `Run` | the engine's run context |

### What belongs to the driver

Everything else. `from.HTTP` has a URL, headers, retry, a `RateLimiter`,
pagination, `Format` and `Records`; `from.Files` has a path, a format and a
`Store`. `bigquery.Table` has a project, a dataset, a table, GCS staging,
`ClusterBy`, partitioning and `CreateSQL`; `to.Files` has a path, `PartitionBy`
and `Compress`. **None of those fields appears on a driver that does not have
them** — that is the difference between one type per driver and a union struct.

---

## 4. The path a record takes

```
from.X.Read()                     iter.Seq2[Envelope, error]  ← lazy
   │
   ├─ Records / decoder           decides what the response carries
   │
   ▼
Transform (per record)            Accept, Rename, Compute, ComputeText,
   │                              IngestionID, IngestionLoadedAt
   │                              SkipRecord drops one; an error fails the run
   ▼
Reduce (optional)                 aggregates the stream; memory is proportional
   │                              to the number of GROUPS, never to the records
   ▼
collect (facade)                  stamps provenance when there is Metadata:
   │                              Provider, Entity, SourceKey, RecordTS
   ▼
to.X.Write()
   ├─ checkColumns                the row against the declaration, both ways
   ├─ prepares the destination    creates it typed when asked
   ├─ checkDeclaredAgainstTable   the declaration against the real destination
   └─ writes                      inline / GCS / MERGE
```

Two things the order explains:

1. **The row that reaches the destination is exactly the one the chain
   composed.** Nothing is stamped afterwards, so the check against `Columns` has
   no special case.
2. **`IngestionID` reads the record in the position it is in.** A `Rename` before
   it forces naming the new field, and naming the old one is an error listing
   what the row actually has.

---

## 5. What the SDK writes

**The columns you composed in `Transform`, and nothing else.** The two the SDK
knows how to write are transformers, placed in the chain like any other:

```sql
ingestion_id        STRING    NOT NULL,
ingestion_loaded_at TIMESTAMP NOT NULL
```

```go
sdk.IngestionID("provider", "entity", "source_key", "time"),
sdk.IngestionLoadedAt(),
```

`sdk.IngestionID` reads its four components from **fields of the record**. They
build the id and do not become columns by themselves — whoever wants `provider`
and `entity` in the table composes them with `Compute`, like any other column.

The `NOT NULL` comes out when `Target.Columns` names the column: declare it, and
the SDK creates the table so it can tighten it; declare nothing, and everything
is inferred nullable.

---

## 6. The rule the architecture rests on

**Go prunes dependencies by imported package, never by used field.** The only
way for a consumer not to pay for a driver is not to import its package.

Measured on `v0.52.0`:

| what you import | packages | AWS | Google |
|---|---|---|---|
| `sdk` | 194 | no | no |
| `sdk` + `from` | 199 | no | no |
| `sdk` + `from` + `to` (files) | 200 | no | no |
| `sdk` + `from/mysql` + `to/mysql` | 202 | no | no |
| `sdk` + `from/postgres` + `to/postgres` | 225 | no | no |
| `sdk` + `from` + `store/s3` | 267 | yes | no |
| `sdk` + `from` + `store/gcs` | 396 | no | yes |
| `sdk` + `to/bigquery` | 458 | no | yes |
| `pycompat` on its own | 68 | no | no |

Before phase 0 it was **458 packages and 21 MB** for any consumer at all,
because the root imported `sdk/load` and that imports BigQuery, Arrow and
Thrift.

There is a test asserting this in `examples/consumer/pruning_test.go`, **with
the control alongside**: whoever imports `to` has to get BigQuery, or the test
would pass with an SDK that carries nothing. CI runs the same check through
`.github/scripts/pruning-check.sh`.

**The rule, in one line: a driver with a vendor SDK behind it lives in its own
package.** That is why BigQuery is `to/bigquery`, Postgres and MySQL each have
their own, and the object stores are `store/s3` and `store/gcs`, while `from`
and `to` hold the ones that need only the standard library.

That was learned the expensive way in `v0.20.0`: `to.BigQuery` and `to.Files`
shipped in the same package, and writing a file compiled Google in — 461
packages and 21 MB where it should have been 195. The pruning test did not catch
it because it only covered the `from` side.

> When adding a driver: if the root starts importing it, or if it shares a
> package with an expensive driver, the property dies in silence. Cover the case
> in the pruning test — **the complete pipeline included, from both sides.**

---

## 7. Errors

| type | means | what to do |
|---|---|---|
| `SourceError` | the source failed in transport | try again later |
| `FormatError` | the data arrived, in the wrong shape | fix the mapping |
| `TargetError` | the destination refused | read `RowErrors` |
| `Rejection` (`sdk.Reject`) | the source sent something that is not data | re-running the same window gives the same result |

`errors.Is(err, sdk.ErrRejected)` separates a refusal from a programming error —
both fail the run, but they ask different things of whoever is on call.

`sdk.SkipRecord`, returned from a `Transformer`, drops **that record** without
failing the run.

---

## 8. Observability

- `Result.Args()` is a fetcher's whole log line.
- `Source.Preview` prints the first N records as a table, to a writer, at the end
  of the extract — including when the source dies partway, which is when you most
  want to see what arrived.
- `-dry-run` extracts, transforms and prints without writing. `-preview N` turns
  the table on without recompiling. `-v` steps up to debug.
- `RunContext` says what the engine knows about the run: `First`, `Attempt`,
  `Trigger`, `LogicalDate`, `Params`. Outside the engine it comes zeroed, and
  ignoring it costs nothing.
- The SDK emits `@brevis:` lines on stdout so the engine can draw the stages
  live. Outside the engine they are absent; see `sdk/telemetry.go`.

---

## 9. Where things are

| you want | it is in |
|---|---|
| the facade, `Pipeline`, `Run` | `sdk/pipeline.go`, `sdk/sdk.go` |
| `Source` and `Target` | `sdk/source.go`, `sdk/target.go` |
| the interfaces | `sdk/internal/core/driver.go` |
| `Transform` and the composers | `sdk/transform.go`, `sdk/expand.go` |
| the aggregators | `sdk/reduce.go` |
| the stackable stages | `sdk/stages.go` |
| the checkpoint | `sdk/checkpoint.go`, `sdk/internal/checkpoint/` |
| the `ingestion_id` | `sdk/internal/core/types.go` (`IngestionID`) |
| the column reconciliation | `sdk/load/columns.go`, `sdk/load/merge_sql.go` |
| the preview | `sdk/internal/core/preview.go` |
| the ingestion transformers | `sdk/ingestion.go` |
| the frozen id formula | `sdk/internal/core/types.go` |
| `CheckColumns` | `sdk/internal/core/metadata.go` |
| the stage telemetry | `sdk/telemetry.go` |
| the cloud backends | `sdk/store/s3`, `sdk/store/gcs` |
| the tests against a real BigQuery | `sdk/load/integration_test.go` |
| the tests against MinIO and GCS | `sdk/from/integration_test.go` |
| the consumer proofs | `examples/consumer/` |
