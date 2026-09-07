# Brevis SDK — examples

Every example is a runnable module of its own. The `go.mod` here points at
`../sdk` through a `replace`, so they compile against the working tree — CI
builds and tests them on every push, which makes them a gate over the API rather
than documentation alone.

```bash
cd examples
go build ./...   # they all compile
go test ./...    # 05 and consumer have real tests
```

## The whole engine, in one workflow

### [full-pipeline](full-pipeline/) — every shape the graph has

```bash
cd examples/full-pipeline
make up      # builds the engine from this tree, brings the stack up
make run     # queues a run
make down
```

Markers, `when:`, `unless_empty:`, `for_each:`, `group:`, `uses:` and edge
labels, in two workflow files and one Go binary. `make empty` and
`make nothing` walk the branches that are usually invisible — the graph fills
with `skipped`, and every skipped step says which step stopped it.

Unlike the examples below, it pins a **published** SDK: it is what somebody gets
from `go get`, not a gate on the working tree.

## Extract

### [01-basic-extract](01-basic-extract/) — the smallest useful case

```bash
go run ./01-basic-extract -url https://example.gov/data.csv
```

`from.CSV` with zero configuration. The CSV's first line becomes the keys;
`NoHeader: true` treats every line as data, with keys `field_0`, `field_1`…

### [02-advanced-extract](02-advanced-extract/) — what matters against a real API

Headers, both timeouts (per attempt and total), retry with backoff, a `Guard`
that rejects a 200 carrying an error body, and rate limiting.

`RateLimiter` accepts anything with `Wait(ctx) error` — `*rate.Limiter` from
`golang.org/x/time/rate` included, without the SDK carrying the dependency.

## Load

### [03-basic-load](03-basic-load/) — writing to BigQuery

```bash
go run ./03-basic-load -project my-project -dataset landing -table raw_data
```

The table has to exist: the SDK does not own your schema. It shows the
functional options and `WithMetadata`, which folds the `_brevis_*` fields into
the payload.

### [07-own-shape](07-own-shape/) — building the row the warehouse expects

When the rows have to match a bronze layer that deduplicates by `ingestion_id`.
You build the shape in one Transformer; only `ingestion_id` and
`ingestion_loaded_at` come from the SDK, and only because a `Metadata` block
asked for them.

That is what gives `ingestion_id` **one** owner: reassembling those columns in
every consumer makes the ids diverge, which is the duplication the contract
exists to prevent.

## Transform

### [09-transform](09-transform/) — the step between extract and load

```bash
go run ./09-transform -dry-run
```

`Without` to drop request metadata, `Rename` to give fields the name you use,
`Compute` to derive, and a function of your own for the rest — with
`sdk.SkipRecord` to filter.

`Metadata.Key` and `Metadata.When` read the record **after** every Transformer,
so they point at the new name. Pointing at the old one is an error listing what
the record actually holds — rather than a short key, which would change every
`ingestion_id` in silence.

## Pipeline

### [04-complete-pipeline](04-complete-pipeline/) — paginated extract → load

Walks an API paginated by `Link: rel="next"` and loads in batches of a thousand,
so memory stays flat regardless of the total size.

The three pagination strategies:

```go
from.HTTP{URL: url, FollowLinks: true}                              // Link header
from.HTTP{URL: url, CursorKey: "next_page", DataKey: "results"}     // cursor in the body
from.HTTP{URL: url, OffsetKey: "offset", PageSize: 100}             // offset
```

All of them stop at `MaxPages` (a thousand by default), so a server that always
announces a next page does not spin forever.

### [08-minimal-fetcher](08-minimal-fetcher/) — a whole fetcher, nothing left out

Four questions, four places: where it comes from, what a response means, what row
it builds, and where it goes with which columns. Flags, `-dry-run`, `-preview`,
logging, retry, table creation and the exit code all come from `sdk.Run`.

### [11-files](11-files/) — files in, files out, no cloud at all

```bash
go run ./11-files
```

The same pipeline serves S3 and GCS by changing one line: the path's scheme says
the backend, and the `Store` is passed in rather than chosen inside the driver.
That is what makes this program compile not one line of AWS or Google.

### [12-postgres](12-postgres/) — Postgres to Postgres, end to end

```bash
docker compose -f ../docker-compose.drivers.yml up -d postgres
export PG_DSN='postgres://brevis:brevis@localhost:55432/brevis_it'
go run ./12-postgres -create-tables
go run ./12-postgres
go run ./12-postgres          # the second run loads zero rows
```

Key-based pagination, `DedupMerge` on `ingestion_id`, and the DDL written by hand
— because the driver creates no table and infers no type.

## Operation

### [05-testing](05-testing/) — how to test code that uses the SDK

```bash
go test ./05-testing -v
```

An `httptest.Server` in place of the real API: fast, offline, deterministic. It
covers a retry on a 503, no retry on a 404, and the error propagating out of your
own processing.

### [06-config-from-env](06-config-from-env/) — configuration from the environment

```bash
export BREVIS_PROJECT=my-project
export BREVIS_DATASET=landing
go run ./06-config-from-env
```

How this usually runs in Kubernetes.

### [10-engine-context](10-engine-context/) — a fetcher that knows it is under Brevis

No flag, no argument, no environment read of its own. The engine injects
`BREVIS_RUN_*` into the step, the SDK picks it up, and `Target.CreateTable` stays
nil and lets it decide. Run by hand, none of that exists and nothing is created.

### [consumer](consumer/) — the gate over the public surface

Written from outside the SDK's module on purpose: it compiles against the working
tree and runs in CI, so a break in the public API shows up here before it becomes
a release. `pruning_test.go` asserts the dependency pruning by counting packages.

### [quickstart](quickstart/) — the engine and a fetcher, together

A compose, a workflow and a fetcher: the shortest path from nothing to a run on
the dashboard.

## Authentication

The load examples need GCP credentials:

```bash
gcloud auth application-default login
# or
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/credentials.json
```
