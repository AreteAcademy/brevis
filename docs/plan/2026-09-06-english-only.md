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
| C | **SDK drivers** — `from/`, `to/`, `extract/`, `pycompat` | 466 | anyone who imports the SDK |
| D | **Living docs** — `COMANDOS`, `KUBERNETES`, `IMAGENS`, `PUBLICAR`, `PARAMS`, `SDK_ARQUITETURA`, `SDK_DECISOES`, `SDK_MATRIZ`, `SDK_NOVO_DRIVER`, `plan.md` | ~700 | a contributor deciding how to help |
| E | **Engine internals** — `internal/`, `cmd/` | 238 | a contributor changing the engine |
| F | **Web** — `web/`, templates | 143 | a contributor touching the UI |
| G | **Infra** — `.github/`, `deployments/`, `migrations/` | 115 | whoever operates or fixes CI |
| H | **Test comments** | 664 | whoever reads a failing test |
| I | **Test function names** | ~235 | whoever reads a failing test's OUTPUT |

Deliberately **not** on the list, with the reason written down:

- **`CHANGELOG.md`, `CHANGELOG-motor.md` and `docs/plan/` (1,797 lines).** They
  record decisions made on a date. Rewriting a record is not translating a
  project. New entries are in English.
- **The historical `docs/SDK*.md`** — `SDK.md`, `SDK_V2.md`, `SDK_V9.md`,
  `SDK_LOAD.md`, `SDK_CONSUMIDOR.md`. Each already carries a header saying it is
  not the current state.

There is one exception worth making inside that rule, in §4.

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

## 6. What done looks like

```bash
# zero, at the repository root
rg -n '^\s*(//|--|#)' --glob '!docs/plan/**' --glob '!CHANGELOG*.md' \
  | rg -c '\b(que|não|para|uma|por|como|sem|isso)\b'
```

Plus: the four gates green, both modules' tests green, and a `CONTRIBUTING.md`
that states the rule so the count stays at zero without anybody policing it.
