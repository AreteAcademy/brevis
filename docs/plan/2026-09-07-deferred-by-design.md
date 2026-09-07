# Deferred by design: what the current shape left room for

**Written on** 2026-09-07 · **Base** engine `v0.7.0`, `sdk/v0.53.0`, `brevis` (py) `0.1.1`
**Status** reference — nothing here is debt

Four things decided *not* to build, each with the reason recorded at the time.
They are collected because a decision scattered across four documents is a
decision nobody finds again, and because each one names the condition that would
change it.

**None of these is a defect.** Nothing behaves wrongly and no document promises
them. This file exists so that when one of them is asked for, the answer starts
from what was already thought through rather than from scratch.

---

## 1. The observed runtime tier

The graph draws a language chip from two sources: what the author **declared**
in the YAML, and what the engine **inferred** from `run:` and `image:`. An
inferred chip is drawn with a dashed border, because "this step runs Python" and
"this command starts with `python`" are different claims.

The third tier — **observed**, the step announcing its own runtime — is designed
for and not built.

```
@brevis:{"type":"runtime","name":"python","version":"3.12"}
```

### Why it is not built

It changes a **published wire protocol** and needs an SDK release to emit it.
The engine already accepts two formats of `@brevis:` with a documented bridge
(see `stages.go` and the `v0.48.0` entry), so the mechanism is proven — the cost
is coordination between two artefacts with separate cadences.

### What it costs when it is wanted

Nothing structural. `Detection.Source` exists, `SourceObserved` is already the
top of the precedence list in `runtimes.Resolve`, and the UI already draws by
source. The work is: emit it from the SDK, parse it in `stageCollector`, and
pass it into `Resolve`.

### What it buys

The chip stops being a guess for anyone using the SDK, and it brings the
version with it — which is item 2.

---

## 2. Versions on the runtime chip

`Python 3.12` is more useful than `Python`, and the current design says
**nothing** rather than guess.

Neither source carries a version reliably: `:3.12-slim` sometimes does and
`:latest` never does, and a command says nothing at all. Guessing a version is
worse than omitting one — a wrong `3.11` on a card sends somebody to the wrong
changelog.

It arrives with the observed tier or not at all. That is the condition.

---

## 3. Reading context from outside the run

Context between steps is scoped to a run, and read only by steps of that run.
That was a decision, taken after weighing an API against the current design, and
it rests on one answered question: *who reads it?* — only the steps.

### What would change it

A notebook, another service, or an ad-hoc script wanting a run's context. On
that day the API earns its keep, and the cost is bounded:

- a **token mechanism**, which this engine has none of today — every route is
  behind a browser session cookie, so it means minting per-run tokens, scoping
  them to a run and step, expiring them, and injecting them into every pod;
- a decision about `brevis run`, which has no server.

**The storage, the keys and both SDKs' surfaces do not change.** `task_runs.saida`
is already the record and `context.get`/`context.set` are already the API. That
is why the decision was safe to take.

---

## 4. State between runs

Yesterday's watermark. The most natural request in the world, and a different
feature.

Context lives inside a run and dies with it. State between runs is persistence,
with its own questions: where it lives, who reads it, what happens when two runs
overlap. Blurring the two turns the context into a database nobody designed —
which is the failure mode this whole design was steering around, and the reason
for the 4096-byte ceiling.

**The answer today is the destination table the pipeline already writes to**, or
the credential store's shape for something genuinely small. If it is ever built,
it is its own feature with its own name, not a widening of this one.

---

## And one that is a product question, not a plan

**Should `cmd/brevis-sdk` exist?**

`run` and `load` now work — the format handling and the stdin reading were fixed
— so the old argument ("cut it back to `extract`, it removes two lies") no
longer applies. What is left is a genuine question with no defect behind it:

`extract` has no twenty-line Go equivalent and is worth keeping. `run` and
`load` overlap with `sdk.Run`, which the SDK's own README presents as the path.
A CLI that duplicates the library is a second surface to keep in step, and it is
now in CI and linted, so keeping it costs maintenance rather than correctness.

Nobody has to decide today. It is written down so the question does not get
rediscovered as a defect.
