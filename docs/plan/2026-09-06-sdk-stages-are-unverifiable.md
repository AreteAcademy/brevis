# A `Stages` pipeline cannot be verified without writing to the destination

**Written on** 2026-09-06 · **Base** `sdk/v0.48.0` · **From** a consumer

Round six. Short, and it is a defect report rather than a design proposal.

`v0.47.0` shipped `Stages` — the ordered list that
[round five](2026-09-05-sdk-stackable-stages.md) asked for. It works: a real
pipeline was rewritten on it and the parts pass their tests.

**It was not adopted**, because two holes together make a `Stages` pipeline
impossible to check before it writes.

---

## 1. `-dry-run` ignores `Stages`

`sdk/pipeline.go:359`, inside `runDryRun`:

```go
data, err := Extract(ctx, p.Source)
...
data = Transform(data, p.Transform...)   // p.Stages is never read
```

The real path (`run`) loops over `p.stages()` and applies each. The dry-run path
does not.

**What the consumer sees.** A pipeline whose stages reduce five million CSV rows
to about five thousand aggregated records:

```
expected:  dry-run … (5,515 records)     {area, year, totals, lat, lng}
actual:    dry-run … (5,238,000 records) {CODGEO_2026:"01001", annee:"2016", …}
```

No warning. The preview showed the raw rows the source produced and called them
records.

This is worse than a missing feature, because `-dry-run` is what people run
*instead of* writing. Its whole purpose is to answer "what would land?", and on a
`Stages` pipeline it answers a different question with the same confidence.

**Fix:** apply `p.stages()` in `runDryRun`, the same way `run` does. If the
counts per stage are cheap to print there too, they turn the preview into the
best debugging tool the SDK has:

```
dry-run … 
  map        8,120,433 → 2,034,112
  aggregate  2,034,112 →     5,515   (5,515 groups)
  map            5,515 →     5,515
```

---

## 2. There is no exported way to run stages

`Stage.apply` (`sdk/stages.go:113`) is unexported, and nothing else exposes it.
So a consumer cannot write:

```go
records := runStagesForTest(source, pipeline.Stages)   // does not exist
```

Combined with §1, the consequence is exact:

> **The only way to see what a `Stages` pipeline produces is to let it write to
> the destination.**

For a pipeline already on a schedule, that is not an option — replacing proven
code with code that cannot be checked is a worse trade than keeping the older,
clumsier version. That is what this consumer did.

**Fix:** either export something small —

```go
func ApplyStages(data *Data, stages ...Stage) (*Data, error)
```

— or, better, fix §1 and let `-dry-run` be the answer. One of the two is enough;
both would be generous.

---

## 3. This is the same shape as an earlier report

[Round five's §6 predecessor](2026-09-05-sdk-stackable-stages.md) noted that
`Records` is an extension point whose input type (`Response`) the consumer cannot
construct, so the logic written there cannot be tested.

The pattern repeats: **a capability ships, and the way to exercise it does not.**
`Records`, and now `Stages`.

It is worth treating as a release criterion rather than as two separate bugs:

> A feature that changes what lands is not done until the consumer can see what
> it produces without writing.

Concretely, for anything that transforms the stream: it appears in `-dry-run`,
and there is some exported way to drive it from a test.

---

## 4. What this cost, so the priority is visible

- One rewrite finished, tested, and parked. It deletes hand-written gzip
  handling, CSV parsing, grouping and summing in favour of `Format: FormatCSV`
  plus `Delimiter` plus `Map`/`Aggregate` — exactly what `Stages` was for.
- The feature has, as far as this consumer can tell, **zero adoption** so far,
  and that is why: it was requested, shipped within hours, and could not be
  turned on.

Neither hole is deep. Both are on the path between shipping the feature and
anyone being able to use it.

---

## 5. One thing the attempt did prove

The rewrite is not wasted, and the design holds up. Reading the CSV by header
name instead of by column index — which `Format: FormatCSV` makes natural —
exposed a latent bug in the consumer's own code: the area column is named
`CODGEO_2026`, carrying the geography vintage, and it will change.

The old index-based reader would have kept working until the column order moved,
then broken silently. The named reader made the fragility visible, and it is now
read by prefix with a loud failure when no such column exists.

That is a point in favour of `Stages` and of the CSV support that came with it —
which is why this report ends as a request to finish them, not to reconsider
them.
