# Alerts and reports: an `alert` pod, and what it is allowed to promise

**Written on** 2026-09-08 · **Base** engine `v0.7.0`
**Status** proposed — not started · **TASK.md #1**

Today one alert exists: the dispatcher calls `Alerts.Failed` when a run gives up
after its last attempt, and a Slack webhook receives it. It is in the
scheduler's process, it fires once per run, and it is not configurable per step.

The request is three things, and they are genuinely different features that
share a delivery channel:

1. **an `alert` pod**, alongside `api` and `scheduler`;
2. **per-step alerting**, declared in the workflow, with a `type` that is `SLACK`
   today and something else later;
3. **a scheduled report** — successes, failures, bytes, rows, durations, and
   insights about the infrastructure.

---

## 1. The finding that shapes everything: today an alert can be lost

`dispatcher.go` calls `d.Alerts.Failed(...)` and, if it fails, logs and carries
on. There is a test for that — `TestAFailureToNotifyDoesNotTakeTheDispatcherDown` —
and it is the right behaviour: a Slack outage must not stop a pipeline.

But the alert is **gone**. No retry, no record, and nothing on the screen to say
it was meant to be sent.

That is the actual defect behind "let us make an alert pod", and it is worth
naming because it decides the architecture. A separate pod that the dispatcher
calls over HTTP would have the same failure with more moving parts.

### The answer is an outbox, and this repository already has one

`queue_items` is a durable, claimable, retryable work queue with `disponivel_em`,
`reivindicado_em` and `reivindicado_por`. The dispatcher claims from it, works,
and releases. **Alerts want exactly that shape.**

```
scheduler                          alert pod
─────────                          ─────────
run fails
  ├─ write task_runs (as today)
  └─ INSERT INTO alerts (…)  ──────►  claim
     in the SAME transaction           deliver to Slack
                                       on failure: back off, retry
                                       on give-up: mark, and it is ON SCREEN
```

Writing the alert in the same transaction as the failure is what makes it
impossible to lose: either the run is recorded as failed and the alert exists, or
neither happened. That is the transactional outbox, and it is the reason for a
separate process rather than a nicety about tidiness.

**What the `alert` pod buys, stated honestly:**

| | |
|---|---|
| the alert survives a Slack outage | the outbox retries; today it is lost |
| the alert survives the scheduler restarting | it is a row, not a goroutine |
| delivery is visible | a row with attempts and a last error, which the UI can show |
| Slack's rate limits stop being the pipeline's problem | one delivery process, not N dispatchers |

What it does **not** buy: independent scaling. Alert volume is a function of
failures, and if failures are high enough to need a second alert pod, the alerts
are not the problem.

---

## 2. Per-step alerting, in the workflow

```yaml
steps:
  - id: extract
    run: python fetch.py
    on_error:
      type: SLACK
```

### The design questions, and the answers

**Where does the destination come from?** Not the YAML. The webhook is a
credential, and a workflow file is written by somebody who is not necessarily
allowed to choose where the company's alerts go — the same argument that made
`BREVIS_POD_ALLOWED_SECRETS` a list the installation controls rather than
something the YAML picks. The YAML says **whether** and **how**; the installation
says **where**, through the env var it already uses.

**`type: SLACK` today, others later.** Validated at publish against a closed
vocabulary, naming what is valid — the same shape as `runtime:` and `tools:`. An
unknown channel refused at publish beats one discovered on the night it was
needed.

**What is `on_error` for a step that retries?** The run-level alert fires when a
run gives up. A step-level one has a choice, and both are defensible:

- **every failed attempt** — noisy, and the flapping case sends four messages;
- **only when the step gives up** — quiet, and it is what the run-level alert
  already does one level up.

Default to the second and make the first opt-in (`on: [attempt]`), because the
cost of the wrong default falls on whoever is asleep.

**Deduplication.** A workflow with ten steps failing on a bad credential sends
ten messages. That is a real complaint about every orchestrator, and the cheap
answer is a per-run cap declared in the outbox rather than in Slack.

---

## 3. `ALERT` and `INSIGHTS` are two features, and the plan splits them

The request proposes one concept, `report`, with `type: ALERT | INSIGHTS`. They
share a delivery channel and nothing else:

| | ALERT | INSIGHTS |
|---|---|---|
| trigger | an event — a step or a run failed | a schedule — weekly |
| scope | one run | everything in a window |
| data | already recorded | **aggregated, and some of it does not exist yet** |
| urgency | now | Monday |

Unifying them under one type field would make one table serve two access
patterns and one delivery path carry two SLAs. What they should share is the
**channel abstraction** — the thing that knows how to talk to Slack — and that is
worth building once.

### The half that cannot ship yet, and why

| the report promises | where it would come from | exists? |
|---|---|---|
| successes, failures | `runs.status` | **yes** |
| max / avg / min duration | `runs.iniciado_em`, `terminado_em` | **yes** |
| rows | `task_runs.etapas` → the SDK's `numeros.rows` | **in JSONB, per step** |
| bytes | the same, `numeros.bytes` | **only when a driver reports it** |
| infra insights (CPU, memory) | nothing collects this | **no** |

Two problems there.

**Rows and bytes live in a JSONB blob, keyed by whatever the SDK happened to
publish.** Aggregating a week of those means scanning `task_runs` and digging
into JSON per row. It works, and it is the wrong shape for a weekly rollup —
which is an argument for a small rollup table written as runs finish, not for a
heroic query.

**Infra insights have no source at all.** This is where the plan says
**not yet**: item #2 in `TASK.md` is the metrics pipeline, and building a second
one here to be replaced in a month is the waste worth avoiding.

So: **ALERT ships first and completely. INSIGHTS ships after observability**, and
until then the report does not promise a number it cannot produce. A report with
`cpu: 0` because nothing measures CPU is exactly the "number that is always zero"
this project refuses.

---

## 4. On the screen

The request says the flow has to appear in the UI, and it is the part that keeps
the feature honest: an alert nobody can see is indistinguishable from an alert
that was never sent.

- **On the run**: whether an alert was raised, whether it was delivered, how many
  attempts it took, and the last error if it never arrived.
- **On the node**: a step with `on_error` declared shows it — the same way the
  runtime chip shows what the step runs in, and drawn as a *declaration* rather
  than a fact until it fires.
- **The delivery is not the alert.** A row that says "raised, not delivered,
  4 attempts, 403 from Slack" is the most useful thing this feature produces,
  because it is the case where somebody is waiting for a message that will not
  come.

---

## 5. Order of work

| | | |
|---|---|---|
| 1 | the `alerts` outbox table and the write in the same transaction as the failure | nothing is lost from here on |
| 2 | the channel abstraction, with Slack as the first — `notify.Slack` already exists and moves behind it | one place knows how to talk to a destination |
| 3 | the `alert` binary and its deployment, claiming and delivering with backoff | the pod that was asked for |
| 4 | `on_error:` in the YAML, validated at publish | per-step alerting |
| 5 | the UI: raised, delivered, attempts, last error | the flow appears |
| 6 | *(after `TASK.md` #2)* the rollup table, the scheduled INSIGHTS report | the weekly report, with numbers that exist |

Steps 1–3 are the architecture and they are worth doing in that order: the
outbox first means step 3 has something real to claim, and the fake in step 3's
tests is a table rather than an HTTP server.

## 6. How it is proven

- **An alert survives Slack being down.** The channel fails, the row stays, the
  next claim retries, and a test asserts the message eventually arrives.
- **An alert survives the process dying between the failure and the delivery.**
  Kill the alert pod mid-claim; the visibility timeout returns the row, exactly
  as `TestRecoverReturnsADeadWorkersItem` already proves for the run queue.
- **Nothing is written when nothing failed.** The commonest run raises no alert,
  and its rows are byte for byte what they were.
- **A workflow with no `on_error` behaves exactly as today.** The run-level alert
  is unchanged, and a test asserts the existing one still fires.
- **`brevis publish` refuses `type: TEAMS`** while Teams is not implemented,
  naming what is valid.
- **Proof each bites**, per `CONTRIBUTING.md`: make the outbox write
  non-transactional and the "survives a crash" test fails; drop the retry and the
  "survives Slack being down" test fails.

## 7. What this plan does not do

- **It does not add a message broker.** Postgres is already the queue, the
  claim, the backoff and the visibility timeout. A second one would be a second
  thing to operate for a feature measured in messages per day.
- **It does not let the YAML choose the destination.** See §2.
- **It does not promise infra numbers before something measures them.** See §3.
