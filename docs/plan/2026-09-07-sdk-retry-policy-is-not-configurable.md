# The scheduler's retry policy cannot be changed, and its default is 3 seconds

**Reported by** a consumer (`zarv-data-pipeline`) · **Found on** engine `0.7.0`

## Context

We are migrating 51 workflows off a previous orchestrator. 44 carry an active
cron; 41 are now registered in Brevis. While auditing what the conversion lost,
we compared retry behaviour, and it is the one place where the new semantics are
materially weaker than the old — not differently shaped, weaker.

## What the old flows declared

**34 tasks** across the 51 flows declared a retry, and every single one declared
the identical policy:

```yaml
retry:
  type: constant
  maxAttempt: 3
  interval: PT3M
```

Three attempts, **three minutes apart**. A window of about six minutes.

That number was not arbitrary. These pipelines fail for one reason above all
others: a transient upstream. A rate-limited vendor API, a provider that answers
HTTP 200 with a plain-text "you have reached your request limit", a BigQuery
quota blip. Three minutes is roughly how long those take to clear.

## What the scheduler gives

From `internal/scheduler/dispatcher.go`:

```go
if c.MaxAttempts <= 0 { c.MaxAttempts = 3 }
if c.BackoffBase <= 0 { c.BackoffBase = time.Second }
…
atraso := d.cfg.BackoffBase * time.Duration(1<<uint(attempt-1))
```

Three attempts, exponential from one second. The attempts land at **0s, 1s and
3s**, and then the run is a definitive failure.

The attempt count matches. The patience does not: six minutes became three
seconds. For the failure mode these pipelines actually have, three attempts
inside three seconds is indistinguishable from no retry — a rate limiter that
needs a minute to reset sees all three attempts inside its window and rejects all
three.

## And neither number can be changed

`scheduler --help` exposes `--interval`, `--concurrency` and `--max-pods`. There
is no flag for either value, and no `BREVIS_*` variable reaches them —
`MaxAttempts` and `BackoffBase` come from `Config.defaults()` and nothing else
writes them on that path.

`--retries` exists, but on `brevis run`, which is the local single-shot runner and
explicitly refuses to operate outside `BREVIS_ENV=local`. It does not reach the
scheduler, which is what runs in dev and production.

Worth flagging because the SDK's own documentation says otherwise: the flag table
in `docs/COMMANDS.md` lists `--retries` without noting it is `run`-only, and the
SDK README states that "in Brevis retry is per run, configured on the scheduler
(`--concurrency`, exponential backoff, 3 attempts)" — which reads as though the
policy were configurable there.

## What we are asking for

**1. Make the policy configurable on the scheduler.** Two flags mirroring the
two fields that already exist:

```
--max-attempts int        attempts per run (default 3)
--retry-backoff duration  base for the exponential backoff (default 1s)
```

We would set `--retry-backoff 60s`, which puts the three attempts at 0s, 1m and
3m — close to what the 34 tasks asked for.

**2. Consider a larger default.** One second is the right default for a queue
whose items are cheap and whose dependencies are local. For a queue whose items
are HTTP fetches against public agencies, it means the retry exists in the code
and not in practice. We are not asking you to pick our number — we are asking
that the default not be the one value that makes the feature inert for I/O-bound
work. Ten seconds would put the attempts at 0s, 10s, 30s.

Neither carries any business rule: this is a queue's retry policy, and every
consumer with a flaky upstream needs the same knob.

## One thing we are NOT asking for

Per-**step** retry. The old flows declared retry per task, and Brevis retries the
whole run — so a `dbt_build` that fails re-runs the fetch ahead of it. We looked
at whether to ask for step-level granularity and decided against it: our fetchers
are idempotent by design (they merge on a deterministic `ingestion_id`), so a
repeated fetch costs time and nothing else, and per-run retry is the simpler
contract. If a consumer ever has a genuinely non-idempotent step, that is when the
ask should be made, with that case as evidence.

## Prompt for the Brevis agent

> On engine `0.7.0`, `internal/scheduler/dispatcher.go` sets `MaxAttempts = 3`
> and `BackoffBase = time.Second`, and computes the delay as
> `BackoffBase * 2^(attempt-1)`. So the three attempts of a failed run land at
> 0s, 1s and 3s, and neither value is reachable from the CLI or the environment —
> `scheduler --help` offers only `--interval`, `--concurrency` and `--max-pods`.
>
> Please expose both: `--max-attempts` and `--retry-backoff` on the `scheduler`
> command, defaulting to today's values so nothing changes for anyone who does not
> set them. And consider whether 1s is the right default for a queue of HTTP
> fetches — a consumer migrating 34 tasks that each asked for 3 attempts three
> minutes apart currently gets three attempts in three seconds.
>
> Two documentation fixes while you are there: the `--retries` row in
> `docs/COMMANDS.md` does not say it is `brevis run` only, and the SDK README
> describes the scheduler's retry as "configured on the scheduler", which it is
> not.
