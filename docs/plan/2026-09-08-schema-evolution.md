# Declared schemas: create the table, then evolve it

**Written on** 2026-09-08 · **Base** engine `v0.11.0`, `sdk/v0.56.0`
**Status** shipped, except Redshift · `TASK.md` #6

Two features that are usually spoken of as one, and the order between them is
forced: a loader that cannot CREATE a table has nothing to evolve.

---

## 1. Where this starts, measured

`sdk.Schema` exists. It is a declaration — name, type, nullability, in DDL
order — and it deliberately does not infer, for the reason written into
`schema.go`: *a field that arrived whole today and fractional tomorrow changes
the column's type with nobody writing anything.*

What it does NOT have, and what only one destination uses:

| | today |
|---|---|
| **BigQuery** | `CreateTable`, `CreateSQL`, DDL rendered from `Schema` |
| **Postgres** | no creation. `table X does not exist. This driver does not create it` |
| **MySQL** | the same message |
| **Redshift** | no creation, no message — it fails on the COPY |
| `Column.Default` | **does not exist** |
| schema evolution | **does not exist**, anywhere |

So "make the schema serve every database" is not a widening of an existing
feature. Three of the four destinations have none of it.

## 2. The declaration gets what a DDL needs

`Column` today is `{Name, Type, Required}`. Nine types, which already cover
string, int and bool and are the minimum set a JSON record produces.

**`Default` is what is missing**, and its shape is the decision worth taking
carefully. Two candidates:

| | |
|---|---|
| `Default string`, raw SQL | powerful, dialect-specific, and a DDL injection point in a field that reads like data |
| `Default any`, a Go literal | safe, rendered per dialect, and cannot express `now()` |

**Neither alone.** A literal covers what people actually declare — `0`,
`false`, `"pending"` — and `now()` is common enough that leaving it out sends
everybody to `CreateSQL`. So: `Default any` for literals, plus one exported
sentinel for the expression that matters:

```go
Schema: sdk.Schema{
    {Name: "status",   Type: sdk.TypeString,    Default: "pending"},
    {Name: "attempts", Type: sdk.TypeInt64,     Default: 0},
    {Name: "seen_at",  Type: sdk.TypeTimestamp, Default: sdk.Now},
}
```

Anything beyond that — a sequence, a computed column, `NUMERIC(18,2)` — is
`CreateSQL`, which goes on existing for exactly that. That boundary is already
the SDK's, and this keeps it where it is.

## 3. One DDL renderer, four dialects

The type table lives in `internal/core`, once, with a per-dialect map beside
it. Not four copies: the count of places a type name is written is the number
of places it can disagree, and this repository has been bitten by that five
times.

|  | Postgres | MySQL | BigQuery | Redshift |
|---|---|---|---|---|
| `string` | `TEXT` | `LONGTEXT` | `STRING` | `VARCHAR(65535)` |
| `int64` | `BIGINT` | `BIGINT` | `INT64` | `BIGINT` |
| `float64` | `DOUBLE PRECISION` | `DOUBLE` | `FLOAT64` | `DOUBLE PRECISION` |
| `numeric` | `NUMERIC` | `DECIMAL(38,9)` | `NUMERIC` | `DECIMAL(38,9)` |
| `bool` | `BOOLEAN` | `TINYINT(1)` | `BOOL` | `BOOLEAN` |
| `timestamp` | `TIMESTAMPTZ` | `DATETIME(6)` | `TIMESTAMP` | `TIMESTAMPTZ` |
| `date` | `DATE` | `DATE` | `DATE` | `DATE` |
| `json` | `JSONB` | `JSON` | `JSON` | `SUPER` |
| `bytes` | `BYTEA` | `LONGBLOB` | `BYTES` | `VARBYTE(1024000)` |

Three of these are choices rather than lookups, and each is written down where
it is made:

- **MySQL `LONGTEXT` and not `VARCHAR`.** A VARCHAR needs a length, and a
  length is a guess about data the SDK has not seen. `LONGTEXT` is the honest
  answer for a landing table; a sized column is `CreateSQL`.
- **MySQL `TINYINT(1)` for bool.** It is what `BOOLEAN` is an alias for, and
  writing the alias would hide that a `2` fits.
- **Redshift has no JSON.** `SUPER` is the closest, and it behaves differently
  enough to be worth naming rather than pretending.

## 4. Evolution: what the market actually does

The convergent answer across Fivetran, Airbyte, Delta Lake and Iceberg is
narrower than "evolve the schema", and the narrowness is the point.

| change | default |
|---|---|
| a **new** column appears | add it, nullable. Nothing that read the old schema breaks |
| a column **disappears** | keep it, stop writing it. Dropping loses history |
| a type **widens** (int → float, int → numeric) | allow |
| a type **narrows** or changes kind | **refuse**, naming both types |
| a column is **renamed** | refuse: without a declaration it is a drop plus an add, and guessing loses a column's history |

That is `additive` as the default, with anything lossy behind an explicit
opt-in.

### The shape

```go
Target{To: postgres.Table{...}, Schema: s, Evolve: sdk.EvolveAdditive}
```

- `EvolveNone` (today's behaviour): a mismatch fails the load.
- `EvolveAdditive` (the default once this ships): add and widen; refuse the
  rest.
- There is deliberately no `EvolveAll`. A mode that drops columns is a mode
  somebody enables during an incident and discovers in a quarter.

**The diff runs BEFORE the load**, not during. A load that half-evolves and
then fails is the worst outcome: the table is neither what it was nor what was
declared, and the next run's diff starts from a shape nobody chose.

**Every change is recorded.** "When did this column appear" is the question
consumers ask after the fact, and a log line that scrolls away does not answer
it.

## 5. The order, and why

| | | why |
|---|---|---|
| 1 | `Default`, and the type table | the declaration has to be able to say it before anything can render it |
| 2 | `CreateTable` on Postgres and MySQL | two real databases in CI, so the DDL is *executed* and not asserted |
| 3 | `CreateTable` on Redshift | no CI instance; the DDL is rendered and reviewed, and that is said out loud |
| 4 | the diff, and `Evolve` | needs a table that exists, which is 2 |
| 5 | recording the changes | needs the diff |

BigQuery already has 1–3 and is the reference for the surface, not a fifth
implementation.

## 6. What would make this a lie

The gate this feature needs is not a unit test of the renderer. Rendering
`CREATE TABLE` correctly and having the database refuse it is exactly the shape
`TestIntegrationCreateSQLRunsTheCallersDDL` found: `CreateSQL` had existed
since `v0.9.0` and had **never been executed against BigQuery**.

So every dialect that has a database in CI gets a test that CREATES the table,
LOADS a batch and reads the rows back. Postgres and MySQL do. Redshift does
not, and the honest version of that is a rendered-DDL test plus a note here —
not a green checkmark that means less than it looks.


---

## 7. What shipped, and the four things only a real server said

Postgres and MySQL create tables and evolve them; BigQuery already created and
is unchanged; **Redshift does neither**, and that is stated rather than hidden —
there is no Redshift in CI, and a rendered-DDL test would be a green checkmark
that means less than it looks. Its dialect table is written and unused.

Four defects were found by executing the DDL rather than by asserting the
string, and each one is now a test:

**1. MySQL refuses a DEFAULT on a TEXT column.** `Error 1101`. Since `string` is
`LONGTEXT` here — a VARCHAR needs a length and a length is a guess — every
string default hit it. The fix is the parenthesised expression default that
8.0.13 added, `DEFAULT ('pending')`, which works where nothing else does.

**2. `ADD COLUMN ... DEFAULT x` BACKFILLS the rows already in the table.**
Postgres has done this since 11 and MySQL does it too, so a row loaded in March
came back claiming a value it never had. Inventing history is what this SDK
refuses everywhere else. The ADD and the SET DEFAULT are two statements now,
and the unit test could not have caught it: the statement it asserted was
perfectly well formed.

**3. MySQL's `ALTER COLUMN ... SET DEFAULT` takes a literal only**, so on a TEXT
column — whose default has to be an *expression* — it is `Error 1101` again.
`MODIFY COLUMN c LONGTEXT DEFAULT ('web')` is the one statement that satisfies
both constraints.

**4. A DEFAULT makes a column OPTIONAL in the row**, and `CheckColumns` refused
exactly that. The feature would have been inert the day it shipped: every load
declaring a default would have failed on "the row does not have it". `CheckRow`
carries the types now, and a column with a default may be absent.

## 8. What is deliberately not here

- **Redshift.** No instance in CI. §6 said this would be said out loud.
- **`EvolveAll`.** A mode that drops columns is one somebody switches on during
  an incident and discovers a quarter later.
- **Renames.** Without a declaration they are a drop plus an add, and guessing
  loses a column's history.
- **Backfilling an added column.** The default describes rows written after it.
  A row that predates the column has no value for it, and NULL is the honest
  answer.
