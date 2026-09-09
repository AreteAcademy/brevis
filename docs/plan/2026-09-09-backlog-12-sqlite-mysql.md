# SQLite and MySQL as the engine's own database

**Written on** 2026-09-09 · **Base** engine `v0.12.0`
**Status** analysis — `NOTES.md` §12, `TASK.md` #11

> *"Precisamos ter SQLite e MySQL como banco de dados para o core brevis ... quando
> nenhum banco foi configurado o SQLite deve assumir o papel do default."*

The ask is right and the cost is not where it looks. This is not "add a driver".

---

## 1. What the engine actually stands on

Measured, not estimated:

| | occurrences |
|---|---|
| `interval` arithmetic | 131 |
| `FILTER (WHERE …)` | 23 |
| `jsonb` | 17 |
| `ON CONFLICT` | 8 |
| `array_agg` | 6 |
| **`FOR UPDATE … SKIP LOCKED`** | **5** |
| `LATERAL` | 4 |
| `generate_series` | 3 |
| `date_trunc` | 3 |

Six files import `pgx` directly, and there are eleven migrations written in
Postgres's dialect.

## 2. The one that is not a dialect problem

`FOR UPDATE SKIP LOCKED` is not a convenience. `internal/queue`'s own comment
says what rests on it: *"several dispatchers compete over the same table without
blocking each other and without handing out the same item twice."* The
dispatcher's concurrency, the visibility window, the orphan sweep and the
`--concurrency` ceiling are all consequences of that one clause.

- **MySQL 8.0** has `SKIP LOCKED`, with different syntax and different locking
  semantics under `REPEATABLE READ`. Portable in principle; the guarantee has to
  be **re-derived**, not assumed.
- **SQLite has nothing of the kind.** It has one writer at a time and no row
  locks. A queue there is a different algorithm — a single-writer claim under
  `BEGIN IMMEDIATE`, which is correct and is *not the same queue*.

So a second backend is not a translation layer. It is a second proof that no run
is handed out twice.

## 3. What each one is actually for

They are not the same request wearing two names, and separating them is what
makes this tractable.

**SQLite is for `brevis` on a laptop, and for a single-node install.** No
container, no connection string, `brevis serve` and a file. That is a real and
large win for adoption — the quickstart today needs a Postgres — and it does not
need the queue to be concurrent, because there is one dispatcher.

**MySQL is for a customer whose platform team runs MySQL and not Postgres.**
That one *does* need the concurrent queue, and it is where the re-derivation
above has to happen.

## 4. The order that makes this cheap

1. **Name the boundary first.** Six files import `pgx`; the SQL lives in strings
   inside them. Before any second driver, the persistence layer needs a seam —
   and the honest version is that today there is none.
2. **SQLite second, single-writer, and say so.** A default that works with no
   configuration is worth more than a second production backend, and it can
   refuse `--concurrency > 1` with a message instead of pretending.
3. **MySQL third, or never.** It is the expensive half, and its value depends
   entirely on a customer asking. Building it before one does is a second queue
   to keep correct for nobody.

## 5. What would make it a lie

A test suite that runs against Postgres and asserts the others compile. The
queue's guarantee — *N dispatchers, M items, nobody gets the same one twice* —
has to run against **every** backend claimed, under real concurrency, or the
claim is that it was typed.

`internal/queue` already has that test for Postgres. It is the acceptance
criterion for each new backend, unchanged.
