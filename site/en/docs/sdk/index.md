# Go SDK

> Writing a fetcher — extract from a source, transform, and load into a destination.

*https://brevis.sh/en/docs/sdk/ · brevis.sh docs (en)*

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

![The SDK flow: sdk.Extract reads from an HTTP API with from.HTTP, sdk.Transform keeps the accepted fields and sdk.Load writes the rows to a BigQuery table. The three calls run inside your own step.](/assets/flow-sdk.svg)

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
[auto params](/en/docs/parameters/#auto-params). `Auto.Now()` is the clock to read
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
creation. The [client libraries](/en/docs/libraries/index.md) are a different thing —
thin clients that hand a step the run context and clock, in Python today
and in Node.js and Rust later. A step using pandas or dbt wants the
second, not this.

## Parameters that build the pipeline

`p.Run.Params` is readable inside the pipeline, which is too late for a value
that decides what the pipeline **is** — one source per state, one table per
tenant. `Pipeline.Flags` is parsed inside `Run`, so it is too late for the same
reason.

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` and `sdk.ParamList` read the engine's environment first and
`-param name=value` second:

```bash
./fetch-stations -param ufs=SP,RJ
```

The environment wins — under the engine it is the value, and a flag left in a
manifest must not override what the operator typed. With no engine the flag is
what there is, which beats writing `BREVIS_RUN_PARAMS` as JSON on every run.

## The landing layout

A pipeline can land the **same table a gateway lands** — the same columns and
the same `brevis_ingestion_id` — so choosing between the two is a deployment
decision and not a schema decision. A team can read both together, and move
from one to the other without a migration.

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` is nine columns: the eight the layout owns and the one
the producer does. `sdk.Landing` fills them and puts the record, whole, in
`data`. Append your own columns to the schema if you want them, or take
`sdk.LandingControlColumns` and give the record's fields columns of their
own.

### Two shapes: the record whole, or one column per field

What is above is the **document** shape — the record goes into `data` as JSON,
and the table is the same one whatever the record holds. Add
`sdk.LandingColumns()` and each field of the record gets a column of its own:

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

The table is then `sdk.LandingControlColumns` plus your columns — **not**
`sdk.LandingSchema`, which carries `data`:

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**You declare those columns.** The SDK never infers a schema, and this option
does not change that: it decides how the *row* is built, never what the table
is. A gateway infers because its producer is a stranger who posts whatever
they have; here the pipeline's author knows the shape and writes it down, in
a diff somebody reviews.

It is the same shape a gateway lands as
[`shape: columns`](/en/docs/ingestion-sinks/#two-shapes-one-contract), through the
same code — same columns, same values, same `brevis_ingestion_id`.

**A field name has to be able to be a column name**: a letter or underscore,
then letters, digits and underscores. That is BigQuery's rule, the narrowest
of the four destinations, so a name that passes here works everywhere. A
record carrying `my-field` is refused by name, before anything is written. The
document shape has no such rule — a hyphen is a perfectly good JSON key.

**It does not flatten, by default.** A nested object becomes ONE `JSON` column under the
key that held it: `customer` holds `{"id": 7, "uf": "SP"}`, not `customer_id`
and `customer_uf`. An array is `JSON` too. A record that wants one row *per
array element* wants `sdk.ArrayAt` in the source's `Expand` — it runs before
`Landing`, and it is a different operation with a different name.
[`BREVIS_NORMALIZE_DATA`](#one-column-per-nested-field) changes the first half
of that for a whole deployment; an array stays `JSON` either way:

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` stays `NULL`**, never `""`. A field sent as null and a field sent
empty are different facts, and a column cannot tell them apart afterwards.

**`data` becomes yours.** In this shape the layout has no column by that name,
so a producer with a field called `data` — which is exactly what the Bacen
series sends — gets a column of their own with their own value in it. Under
`document` theirs is one key inside the layout's JSON.

### The `brevis_` prefix is yours to change

```
BREVIS_LANDING_PREFIX=acme
```

and the layout's eight columns become `acme_ingestion_id`, `acme_record_key`
and the rest. Unset, they are what they have always been.

**Choose it when the table is created, because changing it later is not a
rename.** Nothing in this SDK ever drops a column, so a stream that switches
prefix does not rename eight — it adds eight and abandons eight. Every row
after that has NULLs in the old set, and

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

partitions on a column nothing writes any more. Rows arrive, the dashboard
goes flat, and nothing logs anything.

So a table that already carries the layout under one prefix **refuses** a load
declaring another, before the extract, naming both. Point it at a new table,
or rename the eight columns yourself first.

**Whatever you type is normalised**, so there is one spelling to get wrong
instead of four:

| you set | you get |
|---|---|
| nothing, or blank | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

Trim `_` from both ends, lower-case, add exactly one. Two values are refused
rather than guessed at: underscores and nothing else, because it would leave
`ingestion_id` — exactly the column `sdk.IngestionID()` writes for every
ordinary pipeline — and anything that cannot begin a column name. **A process
with an unusable value does not start**, which is the only honest outcome: the
alternative is tables named after the prefix you were replacing.

**It is per PROCESS, and that is the point rather than a limitation.** Every
stream in one gateway shares it, so two sinks cannot disagree — and neither
can the schema that declares a table and the transformer that fills it.

**The reserved rule moves with it.** Under `acme_`, a producer sending
`acme_region` is refused, because somebody who can forge a control column
forges a real-looking one. And `brevis_region` becomes an ordinary field.

`data` never carries the prefix. It is the producer's column, not the
layout's.

### One column per nested field

```
BREVIS_NORMALIZE_DATA=true
```

and a nested object's fields get columns of their own, the way pandas'
`json_normalize` does — one level:

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**Pick it before the first table, because it does not only ADD columns.** It
renames them, and nothing in this SDK ever drops one — so the old name stays
with the rows already in it while everything after lands in the new one, and
the query somebody wrote against it keeps running and stops seeing new rows.
Two renames happen:

- `userName` becomes `username`, whether or not anything is nested
- `name`, which held objects, is replaced by `name_first` and `name_last`

A table already carrying either shape **refuses** a load with this on, before
the extract, naming both columns. Point it at a new table, or rename the
columns yourself first.

**`name` can appear both flattened and whole**, and the example above shows
it: the third record carries `name` as a string, so the table has
`name_first`, `name_last` AND `name`. A field that is an object in one record
and a scalar in another produces both.

**One level, and arrays are values.** `{"a": {"b": {"c": 1}}}` is `a_b`
holding `{"c": 1}` — a `JSON` column, not `a_b_c`. `{"a": [1, 2]}` is `a`,
also `JSON`. A record that wants one row per array element still wants
`sdk.ArrayAt`.

**Joined and lower-cased, not split on camelCase.** `userName` becomes
`username` and not `user_name`, because the other reading has no agreed
answer for acronyms — `HTTPStatus`, `userID` — and every library disagrees
about them.

**A collision is refused, never resolved.** `{"name_first": 1, "name":
{"first": 2}}` would make one column from two fields, and Go's map iteration
is randomised: letting one win would give the same batch a different table on
a different run. The refusal names both.

**The reserved prefix is checked on the column, not the field.** A producer
sending `{"brevis": {"stream": "x"}}` would otherwise make `brevis_stream`
through a door the field check does not watch. It is refused, naming
`brevis.stream` so they can find the field.

**It is per PROCESS**, like the prefix, and for the same reason: every stream
in one gateway shares it, so two sinks cannot disagree about a table they
might both write to. It applies to the `columns` shape only — under
`document` the record goes whole into one `JSON` column and there is nothing
to flatten into.

### Text that is already JSON

A column declared `sdk.TypeJSON` receives a JSON **value** — an object, an
array, a number. On BigQuery the row is encoded whole, so a Go `string` there
becomes a JSON string literal and the column ends up holding the TEXT of your
object rather than the object:

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

The column is the right type and the data in it is unreachable, which is the
worst of the three outcomes because nothing fails. **The BigQuery destination
refuses it** from `v0.74.0`, naming the column.

Pass the object itself where you have one. Where what you hold is already
JSON text — a column read out of another database, a response body you do not
want to re-parse — say so:

```go
row["payload"] = sdk.JSONText(body)
```

`encoding/json`'s own `json.RawMessage` works too, and for the same reason:
both write the JSON they hold instead of a quoted string of it.

**Postgres and MySQL are not affected and are not refused.** Their server
parses a string into a JSON column, and always has.

### When a field the table does not have shows up

A producer adds a field and the load stops, naming it: the row carries a
column nothing declared, and writing it to a destination that never mentioned
it would drop it in silence. That refusal is right by default — but on a
landing table it is the one change that cannot break a reader, and the
destination can make it:

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

The batch then **completes** your declaration: a field it carries and the
declaration does not becomes a column, `STRING` unless the field is an object
or an array, and then `JSON`. It is the same rule and the same code a gateway
lands `shape: columns` with, so both produce the same table.

Two other values, and the difference between them is where the column comes
from:

| `Evolve` | adds |
|---|---|
| `sdk.EvolveNone` (default) | nothing. Any difference is refused, naming both sides |
| `sdk.EvolveAdditive` | a column your **declaration** has and the table does not |
| `sdk.EvolveAdditiveFromPayload` | that, plus a column the **batch** carries |

**Only ever `ADD COLUMN`.** A column that disappears from the source stops
being written and stays in the table; nothing renames, nothing drops, and no
type is ever narrowed. That is not a gap — a landing table is history, and the
one alteration that cannot break somebody reading it is a new nullable column.

**It completes a declaration; it does not replace one.** With nothing declared
it is refused before the extract, naming the table: creating a whole table
from a payload is a different decision, and the SDK does not make it. On the
landing layout the harm would be concrete — `brevis_received_at` would come
out as text instead of a timestamp, and `brevis_loaded_at` never travels in
the row at all, so the end-to-end latency measurement would simply be missing.

**The SDK still does not infer.** The type comes from the field's SHAPE, never
from its value: `21129` and `8.89` both declare `STRING`, so the day the
series publishes a whole number nothing changes. A type read off the first
batch is the failure this rule exists to prevent.

**And a `null` decides nothing — but it stays in the row.** Those are two
different questions, and answering them with one answer cost v0.77.0.

A null has no shape, so the **schema** has nothing to read: a field that
arrives `null` declares no column, and the column appears from the first record
that carries a VALUE, with the shape that value has. The other way round, a
null would become `STRING` for want of an answer, and the first array after it
would have the destination refuse the whole batch — with no `STRING` to `JSON`
migration to undo it.

The **row** carries what the producer sent, null included. It has to: a column
you DECLARED in `Target.Schema` that arrived empty must be in the row, or the
check that compares the declaration against it reads a chain that has stopped
producing it — and refuses the load. That is what v0.77.0 did, and what 0.78.0
undid.

The destination settles the rest: a field null in EVERY record of the batch,
with no column in the table, is written nowhere and refuses nothing. A null and
an absent field land the same `NULL`, so nothing is lost.

**A field that changes shape is refused, not absorbed.** If `valor` arrived as
a scalar and made a text column, an object arriving later is a change of kind
and the batch stops — with the column's own comment in the message, so you can
see nobody declared it. Rename the column, or migrate it yourself.

**On BigQuery, two spellings of one name are one column.** BigQuery folds
column case: a batch carrying `nationalID` in one record and `nationalId` in
another declares **one** column, not two, and the spelling that wins is the
first in sorted order — `nationalID`, because `D` sorts before `d`. Not the
first to arrive: the same batch replayed must not create a differently-spelled
column. A declaration that says `createdat` against a table whose column is
`createdAt` is the same column too, so nothing is added and nothing is
refused.

**One record carrying both spellings is refused, naming both.** The
destination has one column and one of the two values would silently win, so
the load stops: rename one of them in `Transform`, or map both onto a single
field. Two *records* each carrying one spelling are fine — that is ordinary
CDC traffic, and it is what this rule exists to land.

**Nothing is migrated.** A table that already has two columns differing only
in case keeps both; no column is renamed and none is dropped.

**Postgres and MySQL are not folded.** A quoted identifier is distinct there,
so two spellings are two columns — on purpose, and the same fetcher pointed at
BigQuery will land them in one.

**Every column a batch created says so, in the table.** A comment on Postgres
and MySQL, a field description on BigQuery, carrying the date:

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

Six months later that is the only thing left that answers "when did this
column appear, and who decided it". A log line has rotated by then.

**It is not a mode.** `Target.Schema` is still you declaring what the table
is — this hands you a layout that already exists instead of making you type
it. Nothing in the SDK behaves differently because you used it, and a
pipeline that declares its own schema is unaffected.

**Write the table name once.** It goes to `Landing`, because it is the second
slot of the id, and to the destination, because that is where the rows go.
**Nothing checks that the two agree**: the `Writer` knows its own table but
exposes it only through `Describe`, which is the name for logs and errors,
and an id built on that would tie every id already written to a log string.
Get them out of step and the rows land correctly with ids minted for a table
nobody wrote to. They will look fine. They will not match the gateway's.

**Three columns are `NULL`, and that is the honest answer.** `brevis_stream`
and `brevis_gateway` name things a pipeline does not have —
`brevis_gateway IS NULL` is how you tell a pipeline's row from a gateway's.
`brevis_received_bytes` is `NULL` too: the gateway's counts what arrived on
the wire, envelope included, and the record's own JSON is around 30% smaller.
One column with two meanings would make a sum across rows from both paths
wrong by whatever share came from which. `length(data)` answers the
pipeline's version exactly.

**Merging needs `DedupKey`, and without it there is no merge at all.** Every
driver matches on `ingestion_id` unless told otherwise, and this layout's
identity column is `brevis_ingestion_id`:

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` is what puts the UNIQUE constraint on the id, and Postgres and
MySQL both refuse to merge without one — `ON CONFLICT` has nothing to match.
A `DedupKey` your `Schema` does not declare is refused before anything runs,
because a merge on a column the table lacks matches nothing, and a merge
matching nothing looks exactly like one matching everything it should.

**Per destination, and today MySQL cannot.** BigQuery has no unique
constraints and refuses the declaration outright. MySQL's `string` is
`LONGTEXT`, and MySQL will not key a `LONGTEXT` without a length — so a
merging landing table cannot be *created* there at all:

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres works, because its `string` is `TEXT`. On MySQL, land with
`DedupNone` and resolve the current version downstream, or create the table
yourself with `CreateSQL` and a sized id column.

**The id is content-addressed.** The same record produces the same id from
either path, and a changed record produces a new one — which is what lets a
re-run be a no-op instead of a duplicate.

## Reference

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — the complete API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — twelve runnable examples
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — version-by-version history
