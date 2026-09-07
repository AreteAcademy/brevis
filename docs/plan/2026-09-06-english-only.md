# English only: closing every open thread

**Written on** 2026-09-06 · **Base** `sdk/v0.51.0`, engine `v0.6.0`
**Status** threads A–J and §4 closed on 2026-09-06; §5 is what is left

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
| H | **Test comments** | 0 | ✅ done |
| I | **Test function names** | 0 | ✅ done |
| J | **Portuguese identifiers in code** | 0 | ✅ done — see §7 |

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

There is one exception worth making inside that rule, in §4 — **now done**.

**`docs/phases/` stays too**, on the same grounds `docs/README.md` already gives
for the specs: they record the orchestrator's build phases on the dates they
happened. The §6 gate command below excludes them explicitly, which it did not
before.

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

**A thread reported at zero was not always at zero.** Finishing H meant running
both detectors over the whole repository rather than over one area, and that
found Portuguese still sitting in three threads that had been closed:
`internal/graph/order.go`'s package doc and `internal/execution/kubernetes/`
(thread E), `sdk/from/many.go` and `sdk/internal/checkpoint` (C), and three
deployment YAMLs plus a compose (G). Plus `examples/` — thirteen runnable
programs and three workflow YAMLs — which no thread had ever named, so no
detector had ever been pointed at it.

The lesson is about scope, not about the detectors: each thread ran its detector
over its own paths, and a path in nobody's thread is in nobody's count.

**Each area lands as its own commit, with the full suite green**: both modules'
tests, `-race` on the engine, `golangci-lint` on both, and the four gates
(`generated-check`, `peso-do-motor`, `pruning-check`, `consumer-check`).

---

## 4. The one exception to "history stays"

`CHANGELOG.md` is not a dead record: it is what somebody reads when an upgrade
breaks them. A contributor hitting the `v0.47.0` rename or the `v0.49.0` CSV
change will find Portuguese exactly when they are least able to guess.

**Done.** `CHANGELOG.md` from `v0.40.0` through `v0.52.0`, and all of
`CHANGELOG-motor.md` — the engine's whole published history starts at `v0.4.0`,
so the cut had nothing older to leave behind. A note at the top of each says the
older entries predate the rule.

Quoted "before" strings stay Portuguese: `"o campo %q vale %v, que não é um
número"` is what the old code printed, and an entry that translates it stops
being the record of what broke.

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
rg -n '^\s*(//|--|#)' \
  --glob '!docs/plan/**' --glob '!docs/phases/**' --glob '!CHANGELOG*.md' \
  --glob '!site/**' --glob '!docs/plan.md' --glob '!docs/gaps-yaml-vs-plano.md' \
  --glob '!docs/SDK.md' --glob '!docs/SDK_V2.md' --glob '!docs/SDK_V9.md' \
  --glob '!docs/SDK_LOAD.md' --glob '!docs/SDK_CONSUMIDOR.md' \
  | rg -c '\b(que|não|para|uma|por|como|sem|isso)\b'
```

**It is at zero as of 2026-09-06.** Plus: the four gates green, both modules'
tests green, and a `CONTRIBUTING.md` that states the rule so the count stays at
zero without anybody policing it.

That command counts **comment lines**, which is what every count in this plan
measured. §7 is what it did not see, and it is closed too. What the two of them
together still do not see is prose inside strings; the operator-facing messages
were swept by hand in §7.

---

## 7. The identifiers, and how they were renamed

Found by finishing H: the comment detectors run over comments, and the code
itself had never been counted. It was not clean — 145 distinct Portuguese
identifiers in the engine, 25 in the SDK, 7 in `examples/`, plus every
identifier in the two JavaScript files and twelve Portuguese file names.

**Done, in five commits.** What is left is the four deprecated SDK aliases —
`CampoJSON`, `ComoCriar`, `CriarPorSQL`, `CriarPorSchema` — which stay by the
policy in the drivers plan: a name that shipped in a published version is kept
as an alias held down by a test, and goes in v1.

### Thread J was reported closed once before it was

Every count in this plan — A through J — came from a detector whose vocabulary
was **typed out by hand**. A hand-typed list has holes in it by construction, and
this one had about forty: `lerNDJSON`, `Alternar`, `Ativas`, `AvancarSlot`,
`Baldes`, `Cliente`, `Recuperar`, `Ambiente`, `temMais`, `carregando`,
`aoEsgotar` and their neighbours all read as English to it.

What closed it is a check that does not depend on anybody remembering a word:
split each identifier into its words, and flag any word absent from
`/usr/share/dict/words` that matches Portuguese morphology. It is noisier — it
flags `DoesNotHandOut` and every `Param` — but its blind spots are not the
author's blind spots, which is the whole point.

It is not wired into CI. It needs a system dictionary a runner may not have, and
the allowlist it would need to stay quiet is the same hand-maintained list that
failed here. `CONTRIBUTING.md` states the rule; this is how to audit it.

### The method, and why it is the inverse of thread H's

A rename tool that walks the source and rewrites **only at code positions** —
never inside a comment, a string, a raw string or a rune literal. Thread H
produced five mangled comments by doing the opposite, so this one cannot see
prose at all. Doc comments naming a moved symbol were then fixed in a second
pass, restricted to unambiguous symbol names.

That restriction is not decorative. The first attempt at the test files ran the
comment-side pass over a map of GENERIC words, and it did what generic
substitution always does here: `"--param %q"` became `"--prm %q"`, the JSON tag
`json:"erro"` became `json:"failure"`, and `"data.accessToken"` became a
different key. Reverted and redone.

The check that closes it is a **literal-by-literal diff** against the previous
commit: parse both versions, compare the set of string and comment spans, and
require every difference to be one this commit meant to make.

### Where the rename stopped, and why

| | stays | because |
|---|---|---|
| `Param`'s JSON keys | `Nome`, `Tipo`, `Descricao` | a Workflow is stored whole as JSON in `workflows.definicao` and `runs.definicao` with no tags, so the Go field NAME was the key |
| `postgres.Stage`'s tags | `nome`, `estado`, `numeros`, `indice`, `em` | the `task_runs.etapas` JSONB column |
| the migrations' columns | `criado_em`, `definicao`, … | the applied schema |
| the checkpoint depot | `_completo`, `parte-%05d.ndjson` | a depot written by a released SDK |
| the graph payload | `nome`, `estado`, `numeros`, `rotulo`, `acao`, `erro`, `tentativa`, `duracao_ms` | the wire between `internal/api/graph.go` and `dag.js` |
| `grafico-dica`, `data-dica` | | a CSS class, a data attribute and the script that reads them, in three files |

The first one needed **holding down**, and that is the expensive lesson of this
thread. `Param`'s fields were renamed before anyone noticed the struct was
persisted: `json.Unmarshal` ignores a key it does not know, so every published
workflow would have come back with a param carrying no name, no type and no
description — no error, no log, a trigger form rendering an empty field, and the
validation refusing a value the author had declared as valid.

It now carries explicit tags pinning the old keys, and
`TestTheParamKeysAreTheOnDiskFormat` asserts all six of them plus the count, so
a seventh field added without a tag fails. Verified it bites by dropping the
tags: three failures, each naming its key.

The last two rows are a different kind of stop. Both sides ship in one image, so
renaming them together is safe in principle — but a browser holding the page
across a deploy keeps the old script and receives the new JSON, and every stage
box loses its name until somebody reloads. Eight words are not worth that; they
move when there is a reason to touch the payload anyway. The reason is written
into `dag.js` rather than here, where the next person to look will be.

### Three defects the sweep turned up

- **`Workflow.Validate` ran the same two checks twice** — `MaxActive < 0` and
  `validateResources` over the workflow and every node — in one function, with
  the second copy in Portuguese. The second could never report anything the
  first had not.
- **`brand.yaml`'s colour error named the Go field**, not the YAML key: `colour
  falha` for a file that says `failed:`. It sent whoever was fixing it looking
  for a key that is not in their file.
- **The operator-facing failure strings were still Portuguese** — `saiu com
  codigo %d`, `morto por SIGKILL`, `execucao orfa`, `comando nao encontrado`.
  They are what somebody reads at three in the morning, so they went with the
  identifiers.
- **The login redirect and the login screen used different query parameters.**
  Thread F renamed the form's field from `de` to `next` on both sides and missed
  the redirect that writes the query string, in another package. An operator who
  followed a deep link while logged out signed in and landed on `/` — the filter,
  the page and the workflow they were looking at gone, with nothing saying why.
  The test that should have caught it asserted only that the `Location` header
  mentions `/runs`, which the broken version does too. The parameter is now a
  constant with both ends reading it, the value is percent-encoded (a
  destination carrying its own query string used to lose everything after the
  first `&`), and the test checks the decoded round trip.
