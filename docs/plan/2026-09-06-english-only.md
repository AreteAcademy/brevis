# English only: closing every open thread

**Written on** 2026-09-06 · **Base** `sdk/v0.51.0`, engine `v0.6.0`
**Status** in progress

Contributors are joining from outside Brazil. The project's language is English
— code, comments, identifiers, error messages, tests, commit messages and
documentation. The website keeps its Portuguese, English and Spanish
translations for users; the repository does not.

This is the plan that closes it, plus every other thread left open along the way.
It exists because "translate the project" is not one task: it is nine, with very
different value per line, and doing them in the wrong order means a contributor
still lands on Portuguese while the deepest internals are pristine.

---

## 1. What is left, measured

Not estimated. Counted on 2026-09-06, by a script that looks for Portuguese
function words in comment lines.

| # | thread | lines | who reads it |
|---|---|---|---|
| A | **Front door** — README, issue/PR templates, CONTRIBUTING | 0 | ✅ done |
| B | **SDK root package** (godoc) | 0 | ✅ done |
| C | **SDK drivers** — `from/`, `to/`, `extract/`, `pycompat` | 0 | ✅ done |
| D | **Living docs** — `COMMANDS`, `KUBERNETES`, `IMAGES`, `PUBLISHING`, `PARAMS`, `SDK_ARCHITECTURE`, `SDK_DECISIONS`, `SDK_MATRIX`, `SDK_NEW_DRIVER` | 0 | ✅ done |
| E | **Engine internals** — `internal/`, `cmd/` | 0 | ✅ done |
| F | **Web** — `web/`, templates, the visible UI, and the identifiers `internal/` shares with them | 0 | ✅ done |
| G | **Infra** — `.github/`, `deployments/`, `migrations/`, Makefile, Dockerfile, composes | 0 | ✅ done |
| H | **Test comments** | 664 | whoever reads a failing test |
| I | **Test function names** | ~235 | whoever reads a failing test's OUTPUT |

Deliberately **not** on the list, with the reason written down:

- **`docs/plan.md` and `docs/gaps-yaml-vs-plano.md`.** Listed under D at
  first. `docs/README.md` classifies both as history — the engine's original
  build prompt and an August survey — so they leave D under the rule below.
  Found while doing D.
- **`CHANGELOG.md`, `CHANGELOG-motor.md` and `docs/plan/` (1,797 lines).** They
  record decisions made on a date. Rewriting a record is not translating a
  project. New entries are in English.
- **The historical `docs/SDK*.md`** — `SDK.md`, `SDK_V2.md`, `SDK_V9.md`,
  `SDK_LOAD.md`, `SDK_CONSUMIDOR.md`. Each already carries a header saying it is
  not the current state.

There is one exception worth making inside that rule, in §4.

**A third exception, from G.** The migrations' COLUMN names stay Portuguese —
`criado_em`, `definicao`, `ultimo_slot` and their neighbours are an applied
schema, and renaming them is a migration against a running deployment rather
than a translation. Their comments are English.

`brand.yaml`'s keys are the counter-example, and were renamed **with a fix
attached**: decoding ignored an unrecognized key, so the rename alone would have
quietly reverted every existing file to the default identity. `KnownFields` now
makes an unknown key an error that names it, which also closes the older defect
of a typo doing the same thing.

**A second exception, found while doing C.** The checkpoint's ON-DISK FORMAT
stays Portuguese while the code around it is English: the file `_completo`, the
part pattern `parte-%05d.ndjson`, and the manifest keys `versao`, `registros`,
`partes`, `numeros`. They are data, not prose, and a depot written by a released
SDK has to stay readable.

Two blind renames went at those keys during C. The count test caught
`registros`. Nothing would have caught `numeros`, and that is the expensive
one: without it `UseNumber` never turns on, `19.0` comes back as `19`, and every
`ingestion_id` of a resumed attempt differs from the first attempt's -- with no
error and no log, surfacing as a duplicated row after the next merge.
`TestTheManifestKeysAreTheOnDiskFormat` now pins all five names and says what
each one costs.

---

## 2. The order, and why it is not by size

The first version of this work went by file size. That is the wrong metric: it
translated 44 lines of `internal/core/types.go`, which no consumer ever opens,
before 27 lines of `target.go`, which is the first thing on pkg.go.dev.

The order is **by who reads it, soonest**:

```
A. front door        →  a contributor's first five minutes   [done]
B. SDK root          →  the godoc page a consumer opens      [done]
C. SDK drivers       →  the packages they import next
D. living docs       →  how to run it, how to add a driver
E. engine internals  →  the code a contributor changes
F. web               →  the code a contributor changes
G. infra             →  what they read when CI is red
H. test comments     →  what they read when a test is red
I. test names        →  the same, one line earlier
```

H and I come last on purpose, and it is not because they do not matter. A test
name is read at the exact moment somebody is already confused, so it matters a
lot — but nothing depends on it, the diff is enormous, and doing it early would
bury every other change in noise for a week.

---

## 3. How each line is translated

Comment by comment, reading it first. Not by substitution.

That is a slower method and it is chosen deliberately, because this session
produced two proofs that the fast way is destructive:

- A blind rename turned `// Último a escrever vence` into
  `// Last a escrever vence`.
- Another one changed the **wire protocol** — `{"tipo":"etapa"}` became
  `{"type":"stage"}` — against an already-published engine. The stage-order test
  caught it. Without that test, the phases would have vanished from the screen
  with no error and no log.

The comments in this repository are the part that carries the *why*. Most of them
name the incident that produced the line. Machine-translating them keeps the
words and loses the reason, which is the only thing they were for.

**The detector had a hole, found while doing F.** It looked for Portuguese
function words, so a short comment like `// Card e o painel base` read as
English and passed through threads E and F both. A second detector — `X e o`,
`sao`, `nao`, `ja`, `tambem`, `seria` — found 36 more lines, four of them
user-facing: the whole Slack failure alert, and the fallback brand identity
every installation without a `brand.yaml` gets. Both detectors are run from here
on.

**Each area lands as its own commit, with the full suite green**: both modules'
tests, `-race` on the engine, `golangci-lint` on both, and the four gates
(`generated-check`, `peso-do-motor`, `pruning-check`, `consumer-check`).

---

## 4. The one exception to "history stays"

`CHANGELOG.md` is not a dead record: it is what somebody reads when an upgrade
breaks them. A contributor hitting the `v0.47.0` rename or the `v0.49.0` CSV
change will find Portuguese exactly when they are least able to guess.

**Translate the entries from `v0.40.0` on** — roughly 400 lines, the range
anyone still upgrades across. Everything older keeps a header saying it predates
the rule.

This is the only place where the "records stay as written" rule bends, and it
bends because that document has a live reader.

---

## 5. The threads that are not about language

They have been open long enough to belong in a plan rather than in a message.

### 5.1 `Compute` cannot take a `KeySelector` — the API gap

```go
sdk.Compute("source_key", sdk.Key("latitude", "longitude", "time"))  // does not compile
```

`Compute` wants `func(map[string]any) (any, error)`; `Key` returns
`func(any) (string, error)`. Every caller wraps it in a closure. This was
documented wrongly in two package comments for weeks — the examples did not
compile — which is evidence that the wrapper is not what anyone expects to write.

`FieldSelector` and `KeySelector` are **produced and never consumed** by the SDK:
nothing takes one as a parameter. That is the actual defect.

**Not fixed in this plan.** It changes the composition of `source_key`, which
feeds `ingestion_id`, and it deserves its own round with its own tests. Written
down here so it stops living in a chat message.

### 5.2 The Kubernetes log path has never been exercised

`Logs(pod, follow=true)` is the one piece of the phase pipeline with no proof. The
end-to-end test covers a real SDK binary through a real OS pipe and a real
Postgres, but through the **local** executor.

Closing it needs a cluster. `kind` in CI is the cheap route; the machine that
runs this repository has only real EKS contexts, production included, and those
are not a test bed.

### 5.3 Integration tests that still skip

21 of them, waiting on `GCP_CREDENTIALS` and the `BREVIS_IT_*` variables. Only
BigQuery and GCS are affected; MinIO, Postgres and MySQL already run in CI.

That is the repository owner's to add, not a code change.

### 5.4 The Tailwind gate has a sharp edge

`generated-check.sh` regenerates and then compares against `HEAD`. It cannot tell
"you forgot to regenerate" from "you regenerated and have not committed yet". The
message covers both, and that is enough for now — noted so the next person to hit
it does not think it is broken.

---

## 5.5 Found while checking this plan was complete

Three threads that had never been in any message, found by sweeping the
repository rather than by remembering.

### `brevis-sdk load` reads nothing

`cmd/brevis-sdk/commands.go`. The command's help says:

```
Load NDJSON data from stdin to BigQuery
  cat data.ndjson | brevis load --project my-project --dataset landing --table raw_data
```

The implementation is:

```go
// TODO: read from stdin and parse NDJSON
envelopes := []sdk.Envelope{}
result, err := loader.Load(ctx, envelopes...)
```

It reads nothing, loads zero rows, and reports the result of loading zero rows.
A documented command that succeeds while doing nothing is the worst failure this
project recognises — worse than an error, because the pipe looks like it worked.

**Fix, or delete the command.** Both are better than what is there.

### The queue's tests never run in CI

`internal/scheduler/dispatcher_test.go` gates 14 of its 15 tests behind
`BREVIS_TEST_DATABASE_URL`, which **CI never sets** — only the Makefile does.
So the claim path, the per-workflow limit, the backoff and the orphan recovery
are exercised on a developer's laptop and nowhere else.

This is the third instance of one pattern in this repository: the engine had no
CI at all, the release workflow had never run, and now the queue's tests never
run. Each was invisible because the job was green.

The Integration job already has Postgres. Setting the variable there is one line.

### The documentation site

`site/` — 72 files, and it is where the Portuguese, English and Spanish
translations live. It belongs to the repository owner and is not part of this
plan; noted so "everything" means everything.

---

## 6. What done looks like

```bash
# zero, at the repository root
rg -n '^\s*(//|--|#)' --glob '!docs/plan/**' --glob '!CHANGELOG*.md' \
  | rg -c '\b(que|não|para|uma|por|como|sem|isso)\b'
```

Plus: the four gates green, both modules' tests green, and a `CONTRIBUTING.md`
that states the rule so the count stays at zero without anybody policing it.
