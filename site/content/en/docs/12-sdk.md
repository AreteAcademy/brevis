---
title: Go SDK
description: Writing a fetcher — extract from a source, transform, and load into a destination.
group: SDK and libraries
order: 12
slug: sdk
---

When a step's job is to **fetch data and load it somewhere**, the Go SDK gives
you the whole fetcher in a few lines. It is a separate module, versioned
independently from the engine.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Requires Go 1.23 or newer — the SDK streams rows as `iter.Seq2`.

:::warning Do not use `v0.1.0`
It shipped a `go.mod` pinning a revision that does not exist, and the Go module
proxy is immutable. Start at `v0.1.1`.
:::

## Three steps

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` reads, `Transform` reshapes, `Load` writes. Each takes and returns a
sequence — nothing is materialised in memory all at once.

## The driver is a value, not a setting

`from.HTTP` carries everything an HTTP source needs: URL, headers, retry,
pagination, and what a response means. `from.Files` carries a path and a format.
Neither has to make room for the other's fields, so there is no source struct
collecting forty options of which each driver reads six.

It also decides **what you compile**. Go prunes dependencies by package
imported, never by field used:

| what you import | packages | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | no | no |
| `sdk` + `from` | 194 | no | no |
| `sdk` + `from` + `to` (files) | 195 | no | no |
| `sdk` + `to/bigquery` | 456 | no | **yes** |
| `sdk` + `from` + `store/s3` | 265 | **yes** | no |

A whole file pipeline — read and write — costs 195 packages and no cloud SDK at
all. **A driver with a vendor SDK behind it lives in its own package**, which is
why BigQuery is `to/bigquery` and the object stores are `store/s3` and
`store/gcs`.

## Sources and destinations

| source | package |
|---|---|
| HTTP | `from.HTTP` |
| files (local, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| several sources | `from.Many` |

| destination | package |
|---|---|
| files | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

The path scheme decides the backend, and the `Store` is **passed in** rather
than chosen inside the driver:

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

That is what keeps a local-files program from compiling a single line of AWS or
Google code.

### Publishing to a topic

`to/pubsub` is the one destination that is not a table, and it behaves
differently in a way worth knowing before you use it: **it adds nothing to what
it sends.**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

A subscriber receives your payload, byte for byte, plus exactly the attributes
you named. Nothing else — no `ingestion_id`, no `provider`, no envelope of any
kind.

That is a rule and not a minimalism. A table is created by the pipeline that
writes it, so the SDK may decide what its columns are. **A topic is not**: it
exists before your pipeline does, its subscribers were written first and their
filters were written first. An attribute added on its own would be Brevis
editing somebody else's contract, and the subscriber would find out at three in
the morning.

Three things follow from it:

- **It never creates a topic.** A pipeline that can create one can create the
  wrong one, and unlike a mistyped table nobody notices — the messages go
  somewhere and the subscriber that should have received them stays quiet.
- **`Schema`, `Dedup` and `PartitionBy` are refused**, naming the option. They
  exist for tables: a `Schema` is how a destination creates one, deduplication
  needs a row to replace, a partition is a table's idea. The nearest thing here
  is `OrderingKey`, which is a different one — it groups messages that must
  *arrive* in order.
- **A failure is partial.** Publishing is not transactional, so 48,000 messages
  that fail at 31,000 have *delivered* 31,000. The result reports what actually
  went, and the error says a re-run is a re-delivery — the subscriber has to be
  idempotent, and Pub/Sub is at-least-once anyway.

Runnable, against the emulator:
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub).

## A whole fetcher

`sdk.Run` takes care of flags, `-dry-run`, logging, retry, pagination,
provenance, table creation and the exit code. What is left in the file is only
what is specific to that source:

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## The run context

When the fetcher runs **as a workflow step**, the engine injects into the
environment what it could not otherwise know: whether this is the first run,
which parameters it was dispatched with, which run it is. The SDK reads that in
`Pipeline.Run`:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

Run by hand, `Run` comes back zeroed — reading it is optional, and ignoring it
costs nothing.

With no history, the answer to "is this the first run?" is always **no**:
creating a table without certainty is worse than not creating it.

### The clock

`p.Run.Auto` is what the engine worked out about this run — the
[auto params](/docs/parameters/#auto-params). `Auto.Now()` is the clock to read
instead of `time.Now()`: on a scheduled run it is the slot, so it does not move
when the run is late and does not move when the run is retried three hours
later.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

Run by hand, `Auto.Now()` *is* the wall clock and `Window()` returns `false`, so
local development needs no special case.

## Not to be confused with the client libraries

This SDK is ETL machinery in Go: drivers, pagination, provenance, table
creation. The [client libraries](/docs/libraries/) are a different thing —
thin clients that hand a step the run context and clock, in Python today
and in Node.js and Rust later. A step using pandas or dbt wants the
second, not this.

## Reference

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — the complete API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — twelve runnable examples
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — version-by-version history
