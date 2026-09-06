# SDK — the decisions, and what each one cost

**Valid for** `sdk/v0.52.0` · **Updated on** 2026-09-06

Every line here was once decided the other way. This document exists so the next
session does not undo a lesson that has already been paid for — and so that,
when it does undo one, it does so deliberately and knowing the price.

The format: **the decision**, what was tried before it, and what that cost.

---

## 1. The SDK does not infer a column's type

**Where it holds:** every destination.

On BigQuery, table creation delegates the inference to BigQuery itself: it loads
the batch into a throwaway table with autodetect, reads the schema and overrides
**only** the two columns that are the SDK's. It costs one extra job, on the run
that creates the table.

**Why not infer in Go:** an `encoding/json` `float64` would become a `FLOAT64`
on a column the consumer wanted as `NUMERIC`, and the inference would be back in
through the back door, precisely on the money columns.

**The accepted consequence:** Postgres, MySQL and Redshift have no autodetect
service, so on them the table has to exist or the DDL comes in `CreateSQL`. That
is the decision, not a gap.

---

## 2. The columns come from `Transform`; the destination is declared in `Columns`

**It cost three reversals.** The question "who produces the columns?" changed its
answer in `v0.1.1` (agnostic), in `v0.2.1` (a contract of six fixed columns) and
in `v0.9.0` (agnostic again).

In `v0.9.0` the SDK stopped filling three of the six columns and **nothing on the
consumer's side said so** — the table went on existing, with the columns there,
empty. The symptom arrived days later as a BigQuery type error, and the cause
took three versions to isolate.

**The final shape:** `Transform` composes the row; `Target.Columns` declares the
destination's columns, **the two the SDK fills included**. It is checked against
the row both ways and against the real destination.

**Why `Columns` cannot live in `Transform`:** at the time, the metadata was added
in `Write`, after the whole chain. A `Schema` in the chain that named
`ingestion_id` would have failed — which is why the list used to live
incomplete, and not through carelessness on the part of whoever wrote the
fetcher. Since `v0.24.0` the metadata is a transformer and the row reaching
`Write` already carries it, so the check has no special case left.

---

## 3. `Accept` and `Columns` are two checks, and both are worth having

| | question | catches |
|---|---|---|
| `Accept` | does the source still send what I read? | the vendor dropping a field |
| `Columns` | does the row have the table's columns? | the fetcher forgetting to compose one |

Merging the two to have "a single schema" would trade clarity for a detection
hole.

**On the name:** step (a) was once called `Schema`, and a real fetcher ended up
with **two `sdk.Schema` lines meaning different things**. And it did not go back
to being called `Only`, which was free: the original `Only` **discarded a missing
field in silence**, and handing the same name back with the semantics inverted is
the silent swap `v0.9.0` paid dearly for.

---

## 4. The driver is a value, in subpackages

**It is not an enum.** `Source.Driver string` existed and dispatched nothing — it
was a validation that refused everything but the single implemented driver. It
was removed in `v0.19.0`, along with `DriverHTTP` and `DriverBigQuery`, because
nobody read them.

**Three reasons, in order of weight:**

1. **Dependency pruning.** Go prunes by imported package, never by used field.
   Before phase 0 it was 458 packages and 21 MB for anyone who imported the SDK
   at all, because the root imported `sdk/load` and that imports BigQuery, Arrow
   and Thrift. Today, measured on `v0.52.0`: 194 at the root, 199 with `from`.
2. **Name collision.** `Postgres` exists on both sides with different
   configuration. A single type with both sets of fields brings the dead field
   back.
3. **A compile error.** There is no longer a field in which to write a driver
   that does not exist.

**The same reasoning went one level down in `v0.20.0`.** `from.Files` serves
disk, S3 and GCS, and the choice was "one driver, the path's scheme decides". If
it imported all three backends, reading a local CSV would compile AWS **and**
Google — contradicting reason 1. So the backend became a value too: `core.Store`,
passed in from outside, living in `store/s3` and `store/gcs`. One driver, three
backends, and whoever reads from disk pays 199 packages.

**And `v0.20.0` got the bottom half of that wrong**, which is worth recording:
`to.BigQuery` and `to.Files` shipped in the same package, so writing a file
compiled Google in — 461 packages and 21 MB where it should have been 195. The
pruning test did not catch it because it only covered the `from` side. Fixed in
`v0.21.0`, with the rule written down: **a driver with a vendor SDK behind it
lives in its own package**, and the test covers the complete pipeline, from both
sides.

**The accepted consequence:** `Records` went back to `from.HTTP` in `v0.19.0`,
undoing part of `v0.18.0`. There it had moved up to `Pipeline` because `Source`
was configuration and it was not; with the driver being a value, `from.HTTP`
**is** the whole HTTP source, and a `Pipeline.Records` would be a dead field for
`postgres.Query`.

---

## 5. A field that does nothing is a defect

The class of error this SDK has found most often in itself. The inventory:

| what | how it ended |
|---|---|
| `applyLayout` written and never called | `CreateTable` was a flag with no effect |
| three `With*` with no re-export | unreachable from outside the module |
| `MetadataNamespace` accepted, validated, ignored | removed |
| `SourceKeyField` declared, zero reads | removed |
| `Result.Pages` and `Attempts` always zero | wired to `Stats` |
| `DeleteAfterLoad` documented as "default: true" | a `bool` cannot do that; it became `KeepStagedFile` |
| `core.ExtractOption` with no option, no consumer, no re-export | removed |
| `Source.Driver`, `DriverHTTP`, `DriverBigQuery` | removed in `v0.19.0` |
| the five `FieldSelector` constructors with nothing accepting one | `ComputeText`, in `v0.52.0` |

**The countermeasure that works:** consumer proofs written **from outside the
module**, in `examples/consumer/`. They compile against the working tree and run
in CI, so a surface break shows up before it becomes a release. Three defects got
past tests that lived inside the package and proved what the author could see.

And when one field depends on another, **refuse rather than ignore**. The
`Metadata` block was removed in `v0.24.0` for that reason taken to its limit: a
"switch" with four required fields is not a switch, and `AutoID` — created to
give it a simple state — became the third source of confusion. The two columns
became transformers, and the exception to the rule "the columns come from
Transform" disappeared.

---

## 6. A check that cannot fail is worse than no check

`verify-publication` assembled proxy URLs by hand, with no uppercase encoding,
always got a 404 and ended with `exit 0`. **It passed green for versions.**

Since then: check that the test **bites**. Revert the fix and confirm it fails,
before calling it done. That is how it was established that the positional-MERGE
test really caught the defect, and that the byte count covered both read paths.

The same holds for the pruning tests: the case "whoever imports `to` gets
BigQuery" is the control, without which the test would pass with an SDK that
carries nothing.

The same rule caught two blind renames going at the checkpoint's on-disk format
during the English-only work, and produced
`TestTheManifestKeysAreTheOnDiskFormat` when it turned out nothing guarded the
one key whose loss is silent.

---

## 7. SQL built inside a method that holds a client was never seen by a test

BigQuery's `MERGE` spent **three versions** on `INSERT ROW`, which matches
columns by **position**, under a comment claiming it matched by name. It only
worked because the schemas coincided by accident. On a destination with a fixed
schema, the consumer's `latitude` landed in `ingestion_id`.

**The rule:** `mergeSQL`, `InsertSQL` and `core.Reconcile` are pure functions,
tested under `-short`. And backticks or quotes on **every** identifier: `full`,
`range` and `comment` are reserved and show up in real columns.

---

## 8. Discarding data in silence is the worst way to fail

The asymmetric reconciliation rule, which holds in every record-against-
destination comparison:

- a field in the record the destination does not have → **an error** naming the
  field;
- a column in the destination the record does not carry → NULL, legitimate;
- an incompatible type → **an error** naming the column and both types.

Vanishing with no signal is worse than failing loudly. The way out for whoever
really did mean to discard is `Without` in `Transform`, which says so out loud.

---

## 9. Zero records is a result, not a failure

Only `200` used to pass; `201`, `204` and `206` failed the run with `http NNN`. A
vendor answering `204` on an empty window turned the pipeline red.

Today **every 2xx** reaches `Records`, because what those codes mean is the
vendor's convention and only the fetcher knows. Non-2xx stays as it was.

And validation runs **per response**, not per record: an error response carries
zero records, so a per-record validator would never be called on it — the failure
would arrive as "0 rows", which says nothing.

---

## 10. A refusal from the source ≠ a programming error

`sdk.Reject` and `errors.Is(err, sdk.ErrRejected)`. Both fail the run, but a nil
map and "the vendor sent HTML instead of JSON" ask different things of whoever is
on call — and re-running the same window only resolves one of them.

`Response.Object()` and `JSON()` return a refusal too: a body that is not what was
expected is the source sending something that is not data, helper in the middle
or not.

---

## 11. What is frozen

Changing any of these breaks idempotence with every previous load, in silence:

| | value |
|---|---|
| the `ingestion_id` namespace | `e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7` |
| the UUID v5 key | `provider\|entity\|source_key\|record_ts` |
| the `Key()` separator | `\|` |
| the checkpoint's on-disk format | `_completo`, `parte-%05d.ndjson`, and the manifest keys |

Checked byte for byte against Python's `uuid.uuid5`, because a row written here
has to match the one a Python fetcher writes for the same record.

`MetadataNamespace` once existed as a configurable option — accepted, validated,
defaulted and **ignored**. A configurable contract is not a contract.

---

## 12. Publishing

**Go's module proxy is immutable.** Deleting the tag in git does not unpublish
the version. `v0.1.0` shipped with a `go.mod` pinning a revision that does not
exist and is broken forever; the `README` says so.

The practical consequences:

- got a version wrong? release the next one. Never try to delete the tag.
- `cmd/brevis-sdk` is a module of its own and compiles against the **published**
  SDK, so every API break fails that CI step once, and the pin only moves after
  the tag exists. That is what it is for — it is what caught the `ExtraMetadata`
  rename and the `extract` signature change.
- an exported name that shipped is kept as a deprecated alias rather than
  deleted: a deletion breaks a consumer's build with an error that names a
  symbol and explains nothing. `from.CampoJSON`, `from/mysql.ComParseTime`,
  `from/postgres.ParaJSON`, `load.ComoCriar` and their neighbours are alive for
  that reason, each held down by a test, and they go in `v1`.
- `pkg.go.dev` renders the README **of the published version**, not of the
  branch. A README fix needs a tag before it shows.

---

## 13. Credentials: a warning is worth more than a store

The SDK stores **no credential of its own accord**. Not on disk, not in the
warehouse, not in an abstracted store — a store only exists where the consumer
hands one over, and it is off by default.

The temptation was strong and had a use case: a vendor with no programmatic
login, whose cookie a human pastes out of the browser and whose expiry slides.
The consumer had solved it by keeping the cookie in a `bronze` table — eleven
copies of a **personal** credential, readable by any `dataViewer` on the dataset.
The dataset had been opened up so people could see vendor data, and whoever
granted that access did not know it had come to include this.

The apparent alternative was for the SDK to offer a decent store. That is not
what it does by default, because of one measurement:

```
1. GET /auth/session with the stored token -> Set-Cookie with a NEW token
2. GET /data with the OLD token            -> HTTP 200
```

**The old token survives the rotation.** Each one is valid for its own window,
counted from when it was issued. So storing nothing works, and the cost is known
and singular: somebody re-pastes the credential once per window, instead of
never.

Trading "never re-paste" for "re-paste monthly" is only honest if the person
knows **when** — otherwise the pipeline dies quietly on the 31st, with a 401 that
does not say the cause is an expiry. That is what `Refresh.ExpiresAt` +
`WarnAfter` deliver in place of storage, and it is worth more: **a store defers
the problem; a warning resolves it.**

And the warning is not just a `slog.Warn`. It goes into
`Stats.CredentialExpiry` as well and rises to the pipeline's line, because a log
line in an hourly pipeline is exactly the kind of thing nobody reads — and an
invisible warning is the same silent death with extra steps. §6 of this document
applies: a check that cannot fail is worse than none, and a warning nobody sees
is its cousin.

**What changed since:** `Refresh.Store` exists, and it is opt-in. It is what
turns the environment variable from holding the ROTATING value into holding a
STATIC seed, pasted once. `from.FileStore` writes an encrypted file into a
directory somebody mounted; `gcs.Credential` writes an object with
`ifGenerationMatch`. Neither is on unless the consumer names it, and without a
key the SDK refuses to turn the store on rather than writing in the clear.

What stays in memory, and only in memory, is the `TTL`: the cache of a login for
an API that rate-limits authentication frequency. Under a lock — not out of
ceremony: without it, N goroutines make N logins, which is exactly what those
APIs block.

---

## 14. The three invariants, closed

[`plan/2026-09-03-sdk-schema-declarado.md`](plan/2026-09-03-sdk-schema-declarado.md)
asked for five invariants. Three stayed open for months, under the heading "where
the discussion continues" — which is where an invariant goes to die. Closed in
`sdk/v0.35.0`.

### I2 — the SDK never infers a schema

BigQuery was the only destination still inferring: `CreateTable` created the
table with its autodetect. Postgres, MySQL and Redshift already refused.

The cost was not theoretical. The column's type came from the **first batch**, so
a field that arrived whole today and fractional tomorrow changed the column's
type with nobody writing anything.

Closed with `Target.Schema` — the same list as `Columns`, with a `Type` on each
entry. `CreateTable` with no `Schema` and no `CreateSQL` is **an error naming
what is missing**. `inferSchema` was deleted.

The decision lives in `load.CreationPlan`, a **pure function**, for the reason
this SDK has already paid once: a decision made inside a method that holds a
client is never seen by a test. An invariant you can only exercise with a live
BigQuery project is an invariant nobody exercises.

### I3 — the divergence shows up before the extract

The declared-against-table check already existed; it ran in `Load`, with the
batch in hand. On a vendor with a quota, getting there means having spent the
**whole quota window** to find out that a column does not match.

Closed with `core.DestinationChecker`, called by `runPipeline` **before**
`Extract`. Implemented by BigQuery, Postgres and MySQL.

It is optional on purpose: a directory of files has no schema to check, and
Redshift would need a running cluster. A destination that cannot check early must
not be forced to pretend it can.

`Load`'s check **stays**, and is not waste: between the two the table can change,
and `Load`'s is the one that decides. What the first one buys is the quota.

### I4 — the partition is declared

It used to be daily on `ingestion_loaded_at`, chosen by the SDK. Now it is
`Target.PartitionBy`, and partitioning by a column the `Schema` does not declare
is an error naming the column.

**Closed with a written caveat:** empty keeps the previous default. The invariant
as written says "the SDK does not choose the layout", and a default is a choice.
The alternative — requiring `PartitionBy` whenever `CreateTable` is on — was
considered and not done: a landing table with no partition costs a full scan on
every `MERGE` the bronze layer runs, and an error that pushes somebody into
writing `PartitionBy` without thinking produces the same partition with more
steps.

### I1 and I5 were already closed, by another route

**I1** ("no column exists in the destination without being written in the
fetcher") arrived in `v0.18.0`, with `Columns` instead of the `Schema{}` block
the spec proposed.

**I5** ("how each column is filled is declared") arrived in `v0.24.0`, when the
metadata became a transformer: *how* each column is filled is in the `Transform`
chain, and not in a per-column `From` field. It is simpler than the proposal, and
was asked for exactly because of that.

---

## 15. Where the discussion continues

Nothing open from the declared-schema spec. What is still unanswered comes from
elsewhere:

- **`v1.0.0`** — the drivers' roadmap points at a `v1.0.0-rc` in phase 5, and
  phase 5 shipped as `v0.33.0` instead. The two reasons are written in §9 of that
  plan: Redshift ships with partial verification, and a `1.0` freezes the
  surface. The deprecated aliases from the English-only work are a second reason
  to keep it open: they are meant to go in `v1`, and going to `1.0` before they
  do would freeze them in.
- **`Execute` installs the process's default logger**, which is an application
  decision and not a library's. Flagged for review before `1.0`.
