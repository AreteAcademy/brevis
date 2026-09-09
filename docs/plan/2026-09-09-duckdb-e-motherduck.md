# DuckDB and MotherDuck, and the one fact that decides both

**Written on** 2026-09-09 · **Base** engine `v0.12.0`, `sdk/v0.58.0`
**Status** analysis — `NOTES.md` §14 and §15, `TASK.md` #14

> *"Precisamos ter connector Source e Load integrado com o DuckDb"* · *"Além do
> dlthub, precisamos integrar com o MotherDuck ... se teremos capacidade de
> rodar brevis + MotherDuck."*

---

## 1. DuckDB already runs under Brevis, today

`duckdb` is already in the tool vocabulary (`runtimes.go:70`), already detected
from a command, and already draws its own chip. A step is:

```yaml
- id: transform
  run: duckdb warehouse.db -c "COPY (SELECT …) TO 's3://…'"
```

That works now and needs nothing. **So the ask is not "can we use DuckDB" — it
is `from/duckdb` and `to/duckdb` in the SDK**, which is a different and much
more expensive thing.

## 2. The fact that decides it

Measured, not assumed:

```
$ CGO_ENABLED=0 go build   # with github.com/marcboeker/go-duckdb/v2
mapping_linux_amd64.go:11:22: undefined: bindings.Type
```

**The DuckDB driver does not compile without cgo**, and it drags **135
packages**.

Both halves matter, and the first is the one that ends the discussion about
where it can live:

- `Dockerfile` builds with `CGO_ENABLED=0`. So does every image the project
  plans to publish.
- `engine-weight.sh` and `pruning-check.sh` both measure with `CGO_ENABLED=0`,
  because that is *what ships*.

A cgo dependency in `sdk/` would mean every consumer of the SDK — including the
ones that read a CSV — needs a C toolchain and a DuckDB shared library at build
time. That is not a ceiling being exceeded; it is a different kind of program.

## 3. So it is a separate module, and the pattern already exists

`sdk/metrics/otelmeter` is a module of its own for exactly this reason: weight
that must never reach anybody who does not ask for it. The same shape applies:

```
sdk/duckdb/          # its own go.mod, its own tag
    from.go          # from.DuckDB{Path: "…", Query: "…"}
    to.go            # to.DuckDB{Path: "…", Table: "…"}
```

| | consequence |
|---|---|
| A fetcher that wants DuckDB | imports one module and accepts cgo |
| Every other fetcher | unchanged, still `CGO_ENABLED=0`, still under its ceiling |
| `pruning-check.sh` | gains a case with its own ceiling, asserting the main SDK did NOT grow |

The last row is the one that keeps this honest. Without it, "we put it in a
separate module" is a claim; with it, it is measured on every push.

## 4. MotherDuck is the same driver with a different connection string

MotherDuck is reached through the DuckDB client, with a token and an `md:`
prefix instead of a file path. So it inherits the constraint above **exactly** —
there is no separate, lighter client to reach for.

That is good news for the shape and bad news for anybody hoping it is cheaper:

```go
to.DuckDB{Path: "md:my_db", Token: os.Getenv("MOTHERDUCK_TOKEN")}
to.DuckDB{Path: "warehouse.db"}
```

One driver, one connector, and the connection string decides local or cloud.
**The answer to "temos capacidade de rodar Brevis + MotherDuck" is yes**, and it
costs exactly what the DuckDB connector costs — because it is the same work.

**Unverified, and stated as such:** whether MotherDuck exposes anything other
than the DuckDB client was not checked against their documentation. If it does,
that path avoids cgo entirely and changes this plan's shape, not its
conclusion. Read that before building.

## 5. What makes this worth doing at all

DuckDB is not another warehouse to add to a list of four. It is the destination
for the case the SDK currently has no answer to: **a pipeline whose output is a
file, queried locally, with no server to run.** `to.Files` writes NDJSON or CSV;
DuckDB writes something you can then query.

And with MotherDuck it becomes the smallest possible "warehouse" a Brevis
installation can point at: no Postgres to operate, no BigQuery project, a token.

## 6. Order

1. **Nothing, until somebody asks.** DuckDB as a *step* already works, and that
   covers the common case at zero cost.
2. `sdk/duckdb` as a module, with the pruning case proving the main SDK is
   untouched.
3. MotherDuck is the same code path, and its own test needs a token — so it is
   the same honest position Redshift is in: written, and stated as unproven
   until there is an instance in CI.
