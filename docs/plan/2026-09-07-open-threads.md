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

## 2. `cmd/brevis-sdk` — two of the six defects survive

Four were fixed during the English sweep. What is left:

| | where | what |
|---|---|---|
| **`run` only reads CSV** | `commands.go` | `Format: sdk.FormatCSV` is hardcoded and there is no `--format` flag. A JSON URL is parsed as CSV and the output is garbage — **no error**, which is the worst shape |
| **the version is a literal** | `main.go` | `version = "0.1.0"` while `VERSION` reads `0.7.0`. Nothing sets it through `-ldflags`, so `brevis-sdk version` has been wrong since the split |

The decision from the previous inventory still stands and is still unmade:
**finish the CLI, or cut it back to `extract`.** `extract` is the one subcommand
that works and has no twenty-line Go equivalent. `run` and `load` overlap with
`sdk.Run`, which the SDK's own README presents as the path.

Cutting is smaller and removes a lie. Finishing is more product. The current
state — a `run` that silently mis-parses — is the one option that is not
defensible.

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

## 4. Smaller, and each one a line or two

| | where | what |
|---|---|---|
| **a CI check that cannot fail** | `.github/workflows/build-site.yml:51` | `if grep … \| head -5; then` — the exit status comes from `head`, which always succeeds. It has been printing "potentially broken links" on zero matches since it was written |
| **two versions to keep in step** | `lib/python-context` | `pyproject.toml` and `src/brevis/__init__.py` both carry the version. The publish gate compares the tag to `pyproject` only, so a drift makes `pip show` and `brevis.__version__` disagree in silence |
| **the CLI reference is written twice** | `docs/COMMANDS.md` vs `site/content/{pt,en}/docs/07-cli.md` | they agree because both came from the same `--help`; nothing keeps them agreeing |

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
