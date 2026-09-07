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

## What is left, and where each one lives now

Split into three, because they are three kinds of work with three different
owners. Each is self-contained: pick one up without reading the others.

| | | honest weight |
|---|---|---|
| [`2026-09-07-proofs-that-need-infrastructure.md`](2026-09-07-proofs-that-need-infrastructure.md) | the Kubernetes log path, the GCP integration tests, the Python skip | **the one that matters.** `Logs(follow=true)` is the only production path with no proof at all |
| [`2026-09-07-the-website.md`](2026-09-07-the-website.md) | Portuguese source, Portuguese `i18n.json` keys, the missing Spanish | the Spanish is the only item in the whole audit that makes a document say something untrue |
| [`2026-09-07-deferred-by-design.md`](2026-09-07-deferred-by-design.md) | the observed runtime tier, versions on the chip, context outside a run, state between runs | **not debt.** Decisions with their reasons and the condition that would change each |

### If only one thing gets done

The `kind` job in CI. Everything else on this list is debt, cosmetics, or
opportunity; that one is a code path running in production with nothing
exercising it — and it is the path that would have caught the
`terminationMessagePath` gap in the commit that introduced it rather than in a
conversation two days later.

### Already done since this audit was written

- `cmd/brevis-sdk`'s format handling (three defects, not the two counted), its
  version, and the fact that the module had never been linted.
- The CI check that could not fail, the version typed twice in the Python
  library, and the CLI reference with no gate keeping its two copies in step.

---

## What this audit found clean

- Every plan's stated steps are done or explicitly dropped with a reason.
- No `TODO`, `FIXME` or `XXX` anywhere in production Go, Python or JS.
- Both language detectors at zero over the whole tree.
- The four gates green, both modules' tests green, `-race` green.
