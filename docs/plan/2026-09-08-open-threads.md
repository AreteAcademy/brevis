# What is still open

**Written on** 2026-09-08 · **Base** engine `v0.9.0`, `sdk/v0.56.0`, `brevis` (py) `0.2.1`

An audit checked against the tree, not against memory. It supersedes
[`2026-09-07-open-threads.md`](2026-09-07-open-threads.md), whose "already done"
section has grown and whose three split-out documents are all still accurate.

The short answer: **one open thread has somebody waiting on it, and it is not
on any list.** The rest is debt, proof and opportunity, in that order of
seriousness.

---

## 1. The scheduler's retry policy — the only one with a consumer blocked

[`2026-09-07-sdk-retry-policy-is-not-configurable.md`](2026-09-07-sdk-retry-policy-is-not-configurable.md)
is a **consumer report**, not a plan, and nothing in it has been done. Checked
today against `cmd/brevis/main.go`:

```
scheduler --help  →  --interval  --concurrency  --max-pods
```

`--max-attempts` exists on `brevis alert`, and `brevis run` has `--retries`.
Neither reaches the scheduler, and nothing anywhere reaches the backoff.

`cmdScheduler` builds `scheduler.Config{Worker, MaxConcorrente}` and sets
neither field, so both come from `Config.defaults()`:

```go
if c.MaxAttempts <= 0 { c.MaxAttempts = 3 }
if c.BackoffBase <= 0 { c.BackoffBase = time.Second }
…
atraso := d.cfg.BackoffBase * time.Duration(1<<uint(attempt-1))
```

So a run's three attempts land at **0s, 1s and 3s**. The consumer migrated 34
tasks that each declared three attempts **three minutes apart**, against
rate-limited vendor APIs. For that failure mode, three attempts inside three
seconds is indistinguishable from no retry: the rate limiter sees all three
inside its window and rejects all three.

**Weight: about an hour.** Two flags, two fields set on a Config that already
has them, two tests, and a decision on the default. The report asks that the default not be the one value
that makes the feature inert for I/O-bound work, and does not ask us to pick
its number.

There is a documentation half too, and it is smaller than the report thought:
`docs/COMMANDS.md`'s `--retries` row sits under `## brevis run`, so it is not
wrong in context. What IS missing is anywhere at all stating the scheduler's
actual policy — `site/content/*/docs/05-scheduler-and-queue.md` has a "Retries"
section that explains persistence and pod names and never says how many
attempts there are or how far apart. An operator cannot find that out without
reading Go.

---

## 2. Three proofs that need infrastructure — unchanged

[`2026-09-07-proofs-that-need-infrastructure.md`](2026-09-07-proofs-that-need-infrastructure.md),
all three still open. Verified today:

| | check | state |
|---|---|---|
| the Python skip | `grep "must not skip" .github/workflows/test.yml` | nothing |
| `kind` in CI | `grep "kind-action\|tags=cluster" .github/workflows/` | nothing |
| GCP credentials | 21 tests still skip | the owner's, not a code change |

The first is **one line** and makes the three-language acceptance criterion
stop being optional — `go test` exits 0 on a skip, so a machine without Python
reports green having proved nothing.

The second is still the one that matters: `Logs(pod, follow=true)` is the only
production path with no proof at all, and every `@brevis:` phase, the SDK badge
and now the context in the termination message arrive through it.

---

## 3. The website's three items — unchanged

[`2026-09-07-the-website.md`](2026-09-07-the-website.md). Verified today:
`site/build.py` still has `IDIOMAS = ["pt", "en"]`, `CONTRIBUTING.md` still
promises "Portuguese, English and Spanish", and `i18n.json` still has 258
Portuguese-named keys.

The Spanish line is the only item in this whole audit that makes a document say
something **untrue**, and correcting the sentence is one line. That
recommendation has not changed.

---

## 4. Datasets and data-aware scheduling — named, no plan

`TASK.md` says this was split out of #3 "and named so it is not rediscovered as
a gap". It was named and the plan was never written: `docs/plan/` has no
dataset document.

A step declaring it produces something, and a workflow triggered when that
something updates. It appeared in the flow-shapes target picture as the
`model_trained` node. It is a **scheduling** feature, not a graph one.

Naming a gap and not writing it down is how a gap becomes a surprise. This is
the entry that keeps it visible until it has a plan of its own.

---

## 5. Housekeeping

`TASK.md` lists four items and does not mention the **auto params**, which
shipped in `v0.9.0` and are now read by all three libraries. The table is the
index; something that shipped and is not on it is something the next audit
rediscovers.

---

## What this audit found clean

- No `TODO`, `FIXME` or `XXX` in production Go, Python or JS.
- Every gate green, both Go modules' tests green with `-race`, the Python
  library green on 3.9 and 3.13.
- Every plan's stated steps done or explicitly dropped with a reason.
- The item NOTES.md §3 asks for at the end — *"o contexto precisa estar salvo no
  banco de dados, e toda execucao precisa ter um ID"* — **already shipped**:
  `task_runs.saida` is JSONB in Postgres, keyed by `(run_id, node_id, attempt,
  map_index)`, and every run carries a UUID. A resumed run seeds from it.
