# What is still open

**Written on** 2026-09-07 · **Base** engine `v0.7.0`, `sdk/v0.53.0`, `brevis` (py) `0.1.1`

An audit of everything worked on, checked against the tree rather than
remembered. It supersedes [`2026-09-06-open-threads.md`](2026-09-06-open-threads.md),
whose §1 and most of §4 are done.

---

## The pattern worth naming first

Two features shipped **switched off**, three days apart, and neither was caught
by a test:

| | how it failed | found by |
|---|---|---|
| the Kubernetes return path | the executor never learned about `OutputPath`, so `context.set()` in a pod wrote where nobody read | a question: *"how does the lib reach the API?"* |
| `Runner.ContextDir` | "empty means off", and nothing outside a test ever set it — so the whole feature did nothing for a user | this audit |

Same shape both times: **every test passed, because every test configured the
thing under test.** The local executor was always wired; the field was always
set. A capability that has to be switched on by a field nobody knows about is a
capability nobody has.

The guard that now exists for the second is the useful artefact:
`TestContextWorksWithoutConfiguringAnything` builds the Runner exactly as
`cmd/brevis` does — with no configuration at all. **Any feature with a default
wants that test**, and the two above are the argument.

Worth checking the same way, and not yet checked: the runtime chips, and the
context on the graph. Both are computed from a definition rather than switched
on by a field, so the shape does not apply — but that is a reading, not a test.

---

## 1. Needs infrastructure that is not ours to add

| | what | who |
|---|---|---|
| **the Kubernetes log path** | `Logs(pod, follow=true)` is the one piece of the phase pipeline with no proof. `kind` in CI is the cheap route; this machine has only real EKS contexts, production included | deferred by decision — "mais pra frente" |
| **21 integration tests** | skip on `GCP_CREDENTIALS` and `BREVIS_IT_*`. Only BigQuery and GCS; MinIO, Postgres and MySQL already run | repository owner |
| **the Python cross-language test** | skips without a `python3` on the runner. CI has one, so it runs — but the skip means a machine without Python reports green having proved nothing | one line in `test.yml` to make it required there |

---

## 2. `cmd/brevis-sdk` — ✅ done

The format handling was three defects, not the two counted: `run` hardcoded
CSV, `extract` mapped an unknown `--format` to CSV in silence, and `extract`'s
help promised auto-detection the SDK does not do. One resolver now decides, and
refuses what it does not know.

The version reads from `runtime/debug` instead of a literal that had been stale
since the split — the same mechanism `sdk.SDKVersion` uses, so nobody types it
and it cannot go wrong.

**And the module had never been linted.** Four errcheck findings sat in it, in
the same file whose `run` silently mis-parsed every JSON URL. It is in CI now.

**The decision from the previous inventory is now moot for the wrong reason to
leave unstated**: `run` and `load` work, so "cut it back to `extract`" no longer
removes a lie. Whether the CLI should exist alongside `sdk.Run` is a product
question, not a defect.

---

## 3. The website

| | state |
|---|---|
| `site/build.py`, `site/README.md`, `site/css/*`, `site/js/*` | source comments in Portuguese, against the rule `CONTRIBUTING.md` states |
| `site/i18n.json` | the **keys** are Portuguese, and they are referenced from `templates/*.html` — renaming touches both sides at once |
| **Spanish** | `IDIOMAS = ["pt", "en"]`, and `CONTRIBUTING.md` promises "Portuguese, English and Spanish" |

`site/content/pt/**` stays Portuguese: that is the translation the rule exists to
protect.

The Spanish gap is the one that misleads, and it has two honest resolutions:
ship the translation, or correct the sentence. Leaving both as they are is the
only option that does not.

---

## 4. Smaller — ✅ done

- **The CI check that could not fail** took its exit status from `head`. It now
  fails the build instead of printing a warning nobody reads.
- **The version was typed twice** in `lib/python-context`. `__version__` reads
  the installed package's metadata, and says `devel` from a checkout.
- **The CLI reference written twice** now has one owner and a gate:
  `.github/scripts/cli-docs-check.sh` reads the subcommands out of the binaries
  and fails if either document has stopped naming one. It compares NAMES, not
  prose — two documents describing a command differently is a judgement call;
  one silently missing a command that exists is not.

---

## 5. Deferred by decision, with the reason recorded

Not oversights. Each is written down where it will be found again.

- **The observed runtime tier** — a step announcing its own language over the
  `@brevis:` protocol, the way the SDK badge does. Needs a wire-format change
  and an SDK release; the model already has `Source` and `observed` at the top
  of the precedence list, so it costs no redesign.
- **Versions on the runtime chip** — `Python 3.12` beats `Python`, and nothing
  reliably carries the version. Guessing one is worse than omitting it.
- **Context read outside a run** — the current design's one real limit. If a
  notebook or another service ever needs it, that is the day the API earns its
  keep, and the cost is the token mechanism rather than a redesign.
- **State between runs** — deliberately not the context feature. Different
  questions, and blurring them makes an accidental database.

---

## What this audit found clean

- Every plan's stated steps are done or explicitly dropped with a reason.
- No `TODO`, `FIXME` or `XXX` anywhere in production Go, Python or JS.
- Both language detectors at zero over the whole tree.
- The four gates green, both modules' tests green, `-race` green.
