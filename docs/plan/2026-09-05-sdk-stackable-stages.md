# Stackable stages: `Transform` and `Reduce`, in any order, as many as needed

**Written on** 2026-09-05 · **Base** `sdk/v0.46.0` · **From** a consumer

Round five. It comes from *using* the `Reduce` that
[round four](2026-09-05-sdk-agregadores.md) asked for and `v0.46.0` shipped — and
finding that it does not serve the case that asked for it.

The defect is small and so is the fix. But writing it made clear that the right
fix is more general than the problem.

This round is written in English, and §6 explains why that matters beyond this
document.

---

## 1. What happened when we tried to use it

`Reduce` runs between `Transform` and `Target`. In the real case, what goes in
are raw CSV rows and what comes out is an aggregated record:

```
in:   <area>;<year>;<indicator>;<unit>;<n>;…      (one of millions)
out:  {area, year, total_a, total_b, total_c, lat, lng}
```

A row's identity — its key and its ingestion id — is derived from **the record
that comes out**. It does not exist before the aggregation.

But transformers run **before** `Reduce`. So they would compute the identity of a
CSV row, which is not the row that lands. And `Reduce` would try to *aggregate*
the identity columns, which means nothing.

Today the only way out is to build identity inside `Fechar`:

```go
Fechar: func(groups) ([]map[string]any, error) {
    ...
    r["key"]           = md5(canonical(r))
    r["ingestion_id"]  = uuid5(namespace, key)   // reimplementing the SDK
}
```

That last line is the problem: `IngestionIDWith` exists precisely so nobody
writes it. **A feature that forces you to work around another feature is not
finished.**

### Why the consumer had not felt this before

Because it was already aggregating — inside `Records`, which runs **before**
`Transform`:

```
Source(Records: aggregates) → Transform(identity) → Target
```

It works, and it is the same logical pipeline. `Records` became a "reduce at the
source" because there was nowhere else. **The SDK already supports the shape; it
just cannot be declared.**

---

## 2. The minimal fix, and why it is not enough

The minimum is a second phase:

```go
Transform:      []sdk.Transformer{ ... },   // over the raw row
Reduce:         &sdk.Reduce{ ... },
TransformAfter: []sdk.Transformer{ ... },   // over the aggregated record
```

It solves the case. But it leaves a question without a good answer: **why two
phases and not three?**

A pipeline that aggregates by day and then by month needs two reductions. One
that re-keys after aggregating needs transform, reduce, transform, reduce. The
number two does not come from anything — it is just the smallest number that
solves the case that showed up first.

---

## 3. The proposal: an ordered list of stages

```go
sdk.Run(sdk.Pipeline{
    Source: ...,

    Stages: []sdk.Stage{
        sdk.Map(filter, normalize),
        sdk.Aggregate(sdk.Reduce{By: sdk.GroupBy("area", "year"), Agg: ...}),
        sdk.Map(project),
        sdk.Aggregate(sdk.Reduce{By: sdk.GroupBy("year"), Agg: ...}),
        sdk.Map(identity...),   // last, and §5 says why
    },

    Target: ...,
})
```

Order becomes **explicit in the list** instead of implicit in the order of struct
fields. That is the change that makes the rest possible.

### Compatibility

`Transform` and `Reduce` stay as shorthand for the common case, and desugar into
`Stages`:

```go
Transform: t,  Reduce: r     ≡     Stages: []Stage{Map(t...), Aggregate(*r)}
```

Declaring `Stages` together with either of them is an **error** — two
descriptions of the same thing, and the loser would lose silently. Same rule
`from.Many` already applies between `Sources` and `Discover`.

---

## 4. What it costs, and what must be written down

**Every `Aggregate` is a barrier.** The `Reduce` field already documents that it
drains the source before the first row reaches the target. With two reductions
there are two drains: nothing flows past the first until it finishes.

**The pipeline stops being a stream after the first reduction.** That is not a
regression — it is inherent to aggregating — but with stacked stages it becomes
easy to forget. The memory ceiling stays the largest stage's: `groups × state`,
never `records`.

**Debugging gets harder, and that is where the next request comes from.**

### `Result` needs per-stage counts

Today `Result` reports the total rows written. With four stages, "5,515 rows"
says nothing about where the others went.

Something like:

```go
Result.Stages = []StageResult{
    {Kind: "map",       In: 8_120_433, Out: 2_034_112},
    {Kind: "aggregate", In: 2_034_112, Out:     5_515, Groups: 5_515},
    {Kind: "map",       In:     5_515, Out:     5_515},
}
```

Without it, "why did six million rows disappear?" turns into manual bisection.
With it, it is one log line.

---

## 5. A guard the SDK should have

**A reduction that receives records that already carry identity is almost always
a mistake.**

If the identity column reaches an `Aggregate`, what happens is that the identity
gets aggregated — and the result is a row whose key describes nothing. It does
not raise; it produces wrong data.

Please make `Reduce` **refuse**, naming it, the way the SDK already does
elsewhere:

> `Reduce` received records that already have `ingestion_id`. Identity describes
> the row that lands, and aggregating after it produces a key that corresponds to
> nothing. Move the identity stage after the last reduction.

That turns the rule in §3 — *identity last* — from a convention into a checked
invariant.

---

## 6. The public API is half Portuguese, and that is my fault

Separate from the request above, and worth more than it.

The consumer is standardising on English, because that is what the market reads.
Looking at the SDK's exported surface with that lens, it is **mixed**:

```go
sdk.Compute("x", fn)          // English
sdk.GroupBy                   // does not exist — it is sdk.Agrupar
sdk.SkipWithout("a", "b")     // English
sdk.Soma("value")             // Portuguese
sdk.MaxPor("name", "year")    // both, inside a single identifier
```

`MaxPor` is the clearest evidence: half English, half Portuguese, in one exported
name.

**I introduced most of this.** Round four proposed `Conta`, `Soma`, `Media`,
`MinPor`, `MaxPor`, `Variancia`, `Desvio`, `Algum`, `Todos`, `Amplitude`,
`Personalizado`, `Agrupar` — and the SDK implemented them faithfully. The same
goes for `Reduce`'s fields: `Por`, `Agg`, `Fechar`, and the `Acumulador`'s
`Iniciar` / `Somar` / `Valor`.

For a library meant for any team anywhere, the exported names are the part
everyone reads. A Japanese or German team reading `sdk.Agrupar("region")` has to
translate before they can guess.

**What I suggest, in order of preference:**

1. **Rename the exported surface to English now**, at `v0.47.0`, with the
   Portuguese names kept as deprecated aliases for one minor version. It is
   cheap today — the aggregators shipped hours ago and this consumer is likely
   their only user.
2. If a full rename is too much, at least fix the **mixed** identifiers
   (`MaxPor`, `MinPor`, `ContaDe`), because those are indefensible under any
   convention.

A suggested mapping, so this is a concrete proposal and not a complaint:

| today | suggested |
|---|---|
| `Agrupar` | `GroupBy` |
| `Conta` / `ContaDe` | `Count` / `CountOf` |
| `Soma` / `Media` | `Sum` / `Mean` |
| `Primeiro` / `Ultimo` | `First` / `Last` |
| `MinPor` / `MaxPor` | `MinBy` / `MaxBy` |
| `Variancia` / `Desvio` | `Variance` / `StdDev` |
| `Algum` / `Todos` | `Any` / `All` |
| `Amplitude` | `Range` |
| `Personalizado` | `Custom` |
| `Mediana` / `Quantil` / `Moda` / `Coletar` / `Distintos` | `Median` / `Quantile` / `Mode` / `Collect` / `Distinct` |
| `Reduce.Por` / `.Agg` / `.Fechar` | `.By` / `.Agg` / `.Finish` |
| `Acumulador.Iniciar` / `.Somar` / `.Valor` | `.Init` / `.Add` / `.Value` |
| `pycompat.Texto` / `TextoOuVazio` | `pycompat.Text` / `TextOrEmpty` |
| `pycompat.JSONCanonico` | `pycompat.CanonicalJSON` |

The Portuguese in *internal* code and in the changelog is a separate question,
and not one a consumer should decide. This request is only about what a caller
has to type.

---

## 7. What I am not asking for

**No joins between stages**, no `Filter` stage type, no declarative aggregation
expressions.

Filtering is already a transformer returning `SkipRecord`. Joining against a
small table is already a map loaded in `Discover` and read in a transformer. Each
of those as a new stage kind adds vocabulary without adding capability.

And there is a larger risk worth writing down: **stacking stages makes it easier
to do in the fetcher what belongs in the destination.**
[Round three](2026-09-05-sdk-agregacao-e-dataframe.md) concluded that aggregation
is usually the warehouse's job and that `Reduce` serves a minority. Stacking does
not change that conclusion — it only makes it more comfortable to ignore.

If this is accepted, the documentation for `Stages` should open by saying that
**two stages is already a lot**, and that a four-stage pipeline is probably
solving the problem in the wrong place.

---

## 8. Summary

| | |
|---|---|
| **defect** | no transform after `Reduce`, and identity is computed over the aggregated record |
| **minimum** | a `TransformAfter` |
| **better** | `Stages []Stage` — explicit order, as many phases as needed |
| **compatible** | `Transform` and `Reduce` become shorthand; declaring both with `Stages` is an error |
| **cost** | every aggregation is a barrier; streaming ends at the first one |
| **needed alongside** | `Result` counting in and out per stage |
| **guard** | refuse a reduction that receives records already carrying identity |
| **doc warning** | two stages is already a lot; four is probably the wrong place |
| **separate** | the exported API is half Portuguese — §6, and it was my doing |
