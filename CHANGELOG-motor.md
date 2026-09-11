# Changelog — the engine

The engine's versions, published as a Docker image (`daniel3843/brevis`). The SDK
has its own, in [`CHANGELOG.md`](CHANGELOG.md): they are two artifacts with
different audiences — one is a Go module somebody imports, the other an image
somebody operates — and that is why there are two lists.

The engine's tag is `vX.Y.Z`, with no prefix; the SDK's carries `sdk/`.

---

## [0.13.0] — 2026-09-11

**One migration** (`00012`), and it backfills: 10.8s for 350,000 task_runs on a
probe. Worth knowing before a deploy window.

### Added: a step can run on a host the engine does not manage

```yaml
steps:
  - id: extract
    host: dlt-runner-01        # instead of `image:`
    run: python pipelines/orders.py
```

The third executor, for the work that cannot be a pod on demand — a licensed
tool, a GPU machine, a VM somebody else administers. `brevis-agent` is the
program on the other side and ships from this repository.

The hosts come from the **installation** and never from the file, for the same
reason `BREVIS_POD_ALLOWED_SECRETS` does: a workflow free to dispatch to an
arbitrary address would be dispatching this engine's credentials to it.

```bash
BREVIS_HOSTS=dlt-runner-01=https://10.0.3.7:9443
BREVIS_HOST_TOKEN=...
```

**`host:` never falls back.** An `image:` with no cluster degrades to a local
process with a warning; a host does not, because it is where the licence lives.
An unknown one fails the step naming what the installation offers.

**The engine still sends no secret value.** `secrets:` crosses as the coordinate
`secret-name/key`, exactly as it reaches the kubelet in a pod, and the agent
resolves it against its own store under its own allowlist. The trade-off is that
the policy now lives in two places, one per host.

What the first version does not give, said here rather than discovered: one
token for every host, no TLS of the agent's own, and a cancel that refuses to
signal a pid the OS has recycled rather than risk killing an unrelated process.

### Added: a step publishes its context on stdout when there is no file

A pod writes to `/dev/termination-log` and the kubelet carries it back. A host
outside the cluster has no equivalent — which was the blocker on the whole
remote executor.

```
@brevis:{"type":"context","value":{"watermark":"2026-03-11T04:00:00Z"}}
```

The pipe that already carries an SDK pipeline's phases carries this too, so
**every executor that streams logs gets a return path for free**. Nothing in a
workflow changes and nothing in a fetcher changes: the libraries write the file
when there is one and print the marker when there is not. When both arrive the
file wins, and the engine states that rather than leaving it to chance.

### Added: the load trend, on numbers every pipeline already produced

A panel under the calendar on a workflow's screen: rows per run, bytes per row,
extract seconds, load seconds. Volume is per **run** and not per day — a
schedule going from daily to six-hourly is not a dataset quadrupling — and the
delta compares against the start of the window rather than yesterday.

It needed a table. Measured on a year of hourly runs: reading the same question
out of `task_runs.etapas` costs 14,913 buffers and 22 ms, against 2,243 and 5 ms
from a narrow one, because `etapas` sits beside `log` and `saida` and reading
nine numbers touches all of it.

### Added: `brevis prune`

The only thing in Brevis that deletes anything, and it never runs on its own.

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune                    # trim at 30 days, purge at a year
brevis prune --purge-after 0    # trim only, never delete a run
```

Two levels, because three quarters of the biggest table is **bulk**: emptying
the log, the phases and the published output takes `task_runs` from 339 MB to
92 MB and deletes nothing. Almost all the value is in the gap between the two
levels, and `--purge-after 0` is a real answer.

The load trend is untouched by either: it has no foreign key to `runs` on
purpose, so purging a year of runs does not shorten every chart.

### Added: `description:` in the workflow YAML

```yaml
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
```

At the top of the workflow's screen. The graph already says what a flow does;
this is the only line that says why it exists.

### Added: dlt in the vocabulary, and the run window as its cursor

`tools: [dlt]` validates, and `dlt pipeline …` and `python -m dlt …` light the
chip on their own. `python my_pipeline.py` deliberately does **not** — a chip lit
from a filename would be a guess sitting beside a badge that cannot lie.

And the engine now sets `DLT_INTERVAL_START` / `DLT_INTERVAL_END` on every
scheduled run. A resource with `allow_external_schedulers=True` takes the run's
window as its `initial_value` and `end_value`, so a backfill of March reads
March rather than whatever the persisted cursor says today — and, because an
incremental with an `end_value` does not touch that state, re-running one slot
is idempotent. Verified against a real dlt 1.28.0.

### Fixed: the SDK badge crossed the card's edge

A long step name pushed it out over the canvas. The badge moved to the status
row, where there was room, rather than truncating the name to fit it: at 230px
`fetch_observations` became `fetch_ob…`, and a card's primary identifier must
not be cut to make space for its metadata.

### Changed: five environment variables are in English

`BREVIS_AUTH_USUARIO`, `BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO`,
`BREVIS_POD_MANTER_EM_FALHA` and `BREVIS_POD_ALLOWED_SECRETS`' siblings became
`BREVIS_AUTH_USER`, `BREVIS_AUTH_PASSWORD_HASH`, `BREVIS_AUTH_SECRET` and
`BREVIS_POD_KEEP_ON_FAILURE`.

**The old names still work**, with a warning naming the new one. They are
accepted until a major version, so nothing has to change on this release.

---

## [0.12.0] — 2026-09-09

No migration.

### Added: a step's own metrics, on the engine's `/metrics`

A pipeline knows things the engine cannot: rows a vendor rule rejected, the age
of a watermark, how many files a bucket held. Those now reach the scrape.

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

```go
sdk.Run(sdk.Pipeline{Meter: &sdk.StdoutMeter{}, ...})
```

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

**Nothing new runs.** The engine already read every step's stdout for `@brevis:`
lines — that is how a pipeline's phases reach the graph — and already had a
meter and a Prometheus endpoint. A step cannot be scraped itself: it lives for
forty seconds in its own pod, so a port it opened would be scraped never. A
Pushgateway or an OTLP collector would each be a component to operate, and the
OTLP exporter was already refused on weight (49 packages, 29 of them protobuf).

The **labels are the engine's** — `workflow` and `step` — because a step cannot
know its own slug, and the run id is absent here as it is everywhere: one
unbounded label is how a metrics backend falls over.

Two things bound what a step can do to this process:

- **A ceiling of 200 distinct names.** `metrics.set(f"rows_{customer}", n)` in a
  loop would otherwise grow the registry until the scheduler dies, taking the
  runs with it. The cap is on names; a name already registered costs nothing to
  write again.
- **An invalid name is refused.** A metric name Prometheus cannot parse does not
  lose one series — it makes the entire scrape fail. Both SDKs refuse it before
  it reaches the pipe, and the engine refuses it again for anything that arrives
  by another route.

A step's metric is prefixed `brevis_step_`, so it cannot land on the engine's
own series: without that a step could declare `brevis_run_total` and have its
numbers added to the engine's, which is a dashboard that lies rather than one
that is missing something.

---

## [0.11.2] — 2026-09-09

No migration.

### Fixed: the account menu shipped OPEN

`<details open>` reached `v0.11.1`. The attribute was added to photograph the
menu and never taken out — so every page loaded with the dropdown expanded,
covering the navigation under it.

### Changed: the sidebar's footer

The installation's phrase moved **inside** the menu, and `Powered by Brevis`
is gone from the sidebar (it remains on the sign-in screen). The phrase was a
paragraph of prose sitting between the navigation and the only two things down
there anybody clicks; inside the menu it is still the customer's voice and
costs nothing until the menu is open.

### Added: a workflow's own statistics

The four numbers the dashboard opens with, scoped to one workflow, plus a
calendar heatmap of the year.

The window is **thirty days**, not the dashboard's twenty-four hours. The two
screens answer different questions: "is the installation healthy right now" is
a day, and "is this pipeline reliable" is not — a daily job has one run in
twenty-four hours, and a success rate over a single sample is not a rate.

**The calendar's colour is the day's worst outcome, not its volume.** GitHub
encodes how much happened because that is its question; the question here is
when a pipeline broke, and a heatmap where a busy Tuesday and a broken Tuesday
are both dark answers neither. Volume is what the bar chart already draws.

Each square links to that day's runs. It is server-rendered SVG like every
other chart here: no library, present in the first response, and it prints.

---

## [0.11.1] — 2026-09-09

No migration. An account menu, a Projects screen that does something, and three
Portuguese strings.

### Added: an account menu at the foot of the sidebar

One button, opening upwards — the sidebar's foot is at the bottom of the
window, and a menu that dropped down would be drawn past it. It holds
**Documentation** always, and **Sign out** only when there is a session: an
installation with no credential has nothing to sign out of.

It is a `<details>`, not a script. The browser gives the open state, the
keyboard, the focus order and the ARIA; `ui.js` adds the two things it does not
— Escape, and closing when the click lands elsewhere.

### Changed: Projects is a screen you can use

A project is a **namespace** for workflows: `UNIQUE (project_id, slug)`, so two
projects can each have a `daily_ingest`. It arrives from
`brevis publish --project`.

And it was used nowhere else. The screen listed four numbers and a date with
nothing to click, the project under a workflow's name was not a link, and the
filter had search, state, active and tag but no project. A namespace that
nothing can be scoped by is a row in a table.

Now every card leads to that project's workflows, the name under a workflow is
a link, and `?project=` joined the filters that were already there — it survives
a search, it survives a sort, and it comes off with a chip like the others. The
screen also says in one sentence what a project *is*.

There is no row of project chips beside the tags, deliberately: a project is a
namespace, not a category, and most installations have one.

### Fixed: three Portuguese strings, and the hole that let them through

`Fechar`, `remover filtro`, and — on the workflows screen, for as long as it has
existed — **`de 5`** where it should read `of 5`.

`ui-language-check.sh` caught none of them. Its pattern for text between tags
excluded `{` and `}`, so a span holding both a word and an interpolation was
skipped entirely: every string that mixes prose with a value was invisible to
it. That is the second time this check has been fixed by a leak getting past it.

Also: a workflow that has run once now reads `1 run`.

---

## [0.11.0] — 2026-09-08

No migration. The change is what the interface LOOKS like, and one message it
writes.

### Changed: the console carries Brevis's identity

It carried Arete Academy's, and its own CSS said so: warm parchment, Cormorant
Garamond, gold, corners of 20 to 28 pixels, a golden halo and a background
grid. That is a brochure's vocabulary on a screen whose job is a table of run
ids, and it was not even this product's brochure.

It now carries brevis.sh's, on a light operational ground. The site is dark by
decision and says why — it is a landing page, read for two minutes. This is a
console, read for hours, in a bright office, beside a browser full of light
tools. So the hues are the site's and the ground is inverted: `#141711`, the
site's BACKGROUND, is this one's text.

The thirteen colours are measured against both grounds and the ratios are in
the CSS. Nothing is below AA, including the state colours, which are used as
text on a pill and not only as a dot.

**Typography** is IBM Plex Sans and Mono, still served from the binary — a UI
that depends on Google Fonts changes typeface halfway down the screen in a
cluster with no route out. It is 92 KB against the 208 KB Inter and Cormorant
Garamond cost, so the image is smaller.

**The corners are square**, 3px, which is what brevis.sh uses and says why:
*"cantos quase retos: a superfície é operacional, não um widget"*. Status dots,
the donut and the switch stay round — squaring a switch makes it stop reading
as a switch.

**The sign-in screen** is the mark, two fields and a button on a flat ground.
The grid, the halo and the ring around the card are gone.

**An installation's own theme still wins.** `internal/branding` overrides the
same tokens it always did, and the old names (`parchment`, `gold`) are kept as
aliases so a `brand.yaml` written against them keeps working.

### Fixed: the run graph sat in an ocean of empty grid

Eight steps drawn in the middle of a thousand pixels. React Flow fits its view
ONCE, when it initialises — which happens on the first render, before the
graph has been fetched — so every workflow inherited the viewport of an empty
frame. The canvas is now sized to the drawing, and the fit is redone when the
drawing changes shape.

### Fixed: five Portuguese strings in the interface

"Em andamento", "em curso", "Buscar workflow", and the column headers "Origem"
and "Criado". The search button said "Get", which is not what it does. The
engine's own `nivel %d:` in a failure message is now `level %d:`.

`CONTRIBUTING.md` has required English since the language sweep and nothing
verified it, which is how these arrived one at a time.
`.github/scripts/ui-language-check.sh` now fails CI on Portuguese in
user-visible text — including in component arguments, which is where two of the
five were hiding and where the first version of the check did not look.

---

## [0.10.1] — 2026-09-08

### Fixed: the run's screen showed a different clock from the step

The auto params are UTC by contract -- stored UTC, injected as
`2026-03-11T01:00:00Z`, read as UTC by both SDKs. The screen rendered them with
`.Local()` and no marker, so a server at UTC-3 showed:

```
adjusted_at   2026-03-10 22:00:00
date          2026-03-11
```

Side by side in the same grid. One day apart, nothing saying why, and a third
answer for anyone who checked `$BREVIS_AUTO_ADJUSTED_AT`.

The auto params and the slot now render in **UTC, marked**. The screen agreeing
with the contract is worth more than it agreeing with the clock of whoever ran
the deployment.

And every other timestamp on the screen now names its clock -- `2026-03-10
22:00:00 -03` rather than `2026-03-10 22:00:00`. `notify/slack.go` had already
learned this and appends `MST` for the same reason: `Local()` is the timezone of
whoever FORMATS, so the same instant reads 22:00 on a laptop and 01:00 in a pod,
and two people comparing one failure at three in the morning disagree about when
it happened.

Nothing about the values changed. `date` is still the UTC day, which for a
`0 22 * * *` in `America/Sao_Paulo` is the following day -- the same as
Airflow's `ds`, and now said out loud in the documentation instead of left to
be discovered.

### Fixed: the site generator hung instead of failing

Writing that documentation split a table in half, and `site/build.py` **looped
forever**. The `|` lines after the paragraph are orphans -- they do not open a
table, because the line under them is not a separator, and the paragraph branch
rejects any line starting with `|`. So nothing consumed the line, `i` was never
advanced, and the generator spun at 100% CPU writing nothing and saying
nothing.

In CI that is a timeout with no message, which is worse than a failure. It now
raises, naming the line and the most likely cause. The same hang was reachable
from a `:::` block with an unknown type and from a stray `---`.

---

## [0.10.0] — 2026-09-08

### Before upgrading

**A failed run now waits longer between attempts.** The default backoff moved
from **1 second to 30 seconds**, so the three attempts land at 0s, 30s and
1m30s instead of 0s, 1s and 3s. Nothing else changes, and
`--retry-backoff 1s` restores exactly the old behaviour.

Two consequences worth weighing before deploying:

- A run that recovers on a retry now takes up to 90 seconds longer.
- A run that is going to fail definitively is **announced 90 seconds later**,
  because the alert is raised on the last attempt only.

No migration.

### Fixed: the retry existed in the code and not in practice

Reported by a consumer migrating 51 workflows. 34 of their tasks declared the
same policy — three attempts, **three minutes apart** — and Brevis gave them
three attempts inside **three seconds**.

That is not a weaker retry, it is no retry. These pipelines fail for one reason
above all others: a transient upstream. A rate-limited vendor API, a provider
answering 200 with a plain-text "you have reached your request limit", a
warehouse quota blip. Those take about a minute to clear, and a limiter that
needs a minute sees all three attempts inside its own window and rejects all
three.

Neither number was reachable. `scheduler --help` offered `--interval`,
`--concurrency` and `--max-pods`; `MaxAttempts` and `BackoffBase` came from
`Config.defaults()` and nothing on that path ever wrote them.

| | |
|---|---|
| `--max-attempts` | attempts per run, counting the first. Default `3` |
| `--retry-backoff` | the first delay, doubled on each attempt after it. Default `30s` |
| `--retry-backoff-max` | ceiling for that delay. Default `1h` |

**Why 30 seconds and not their three minutes.** The number is a platform
default, not one installation's. 30s is the largest value that keeps a
definitive failure's alert inside two minutes, and the smallest where the third
attempt lands outside a one-minute rate-limit window. Whoever needs another
sets `--retry-backoff 1m`, which gives 0s, 1m and 3m.

**Why there is a third flag nobody asked for.** Making `--max-attempts`
configurable made the exponential reachable: `--max-attempts 10
--retry-backoff 60s` puts the last wait at four hours and the whole window at
eight. Past that it overflows — `base << (attempt-1)` comes back negative
around attempt 35 and **zero** from 63, and a zero delay is an instant requeue,
a hot loop against whatever was already failing. The backoff now doubles in a
loop that stops at the cap, which cannot overflow at all.

### Fixed: the policy was only ever written in Go

There was no way to find out what the retry did without reading
`dispatcher.go`. The site's "Retries" section explained persistence and pod
names and never said how many attempts there were or how far apart, and
`docs/COMMANDS.md` did not list the flags because there were none.

Now the scheduler says it at boot, with the times themselves rather than the
two numbers they come from:

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

The flag defaults are read from `scheduler.Defaults()` rather than typed in
`cmd/brevis`, so `--help` cannot describe a policy the dispatcher does not
have. And `cli-docs-check.sh` now compares **flags** as well as subcommands: a
flag missing from `docs/COMMANDS.md` fails CI. Documenting the policy and then
letting the document drift would put it back where it was.

---

## [0.9.0] — 2026-09-08

### Before upgrading

**Run the migrations.** One new column, `runs.auto_params`.

```bash
brevis migrate up
```

Nothing else changes: existing runs keep an empty object, and a workflow that
ignores the new variables behaves exactly as it did.

### Added: auto params

Every run now carries values the engine works out on its own. Nobody declares
them, every run has them, they are on the run's screen and in every step's
environment as `$BREVIS_AUTO_*`.

The one that matters is `adjusted_at`, and the bug it removes is common enough
to be worth naming. A fetcher reads `now()`, subtracts its window and asks the
vendor for the last two hours. On a run that starts on time that is right. On a
run the queue delayed by forty minutes it is forty minutes wrong — and those
forty minutes belong to **no run at all**, because the next slot reads its own
`now()` too. Nothing fails, and the gap is found weeks later.

So the engine hands over a clock instead:

| | |
|---|---|
| `adjusted_at` | the clock to read instead of `now()`: the slot when there is one, the start otherwise |
| `date` | `adjusted_at` as `YYYY-MM-DD`, UTC |
| `delay_seconds` | how late this attempt was against its slot |
| `previous_error` | the run before this one did not succeed |
| `scheduled_at`, `started_at` | the slot, and when this attempt began |
| `interval_start`, `interval_end` | the window this run covers, end excluded |
| `previous_success_at` | the slot of the last run that did succeed |

The window comes from the **cron**, not from the history: a backfill of a slot
from March produces the window March had, not the window this workflow's runs
happen to describe today. A pipeline that asks for `[start, end)` never overlaps
and never gaps, however late it runs and however often it retries.

They are a **snapshot**, computed when the run starts and stored on it — which
is what the new column is for. `previous_error` is a fact about the instant this
run began; recomputing it tomorrow, after the previous run was retried and
passed, would answer a different question with the same name.

The optional ones are **absent** from the environment rather than empty. A
variable that is always there and sometimes blank makes every reader write the
same two-line check; an absent one makes `${X:-default}` work.

`sdk/v0.56.0` reads them as `p.Run.Auto`, so a Go fetcher does not parse
anything.

---

## [0.8.0] — 2026-09-07

Four features and one apology. Read **Before upgrading** first: this release
changes what an existing workflow does.

### Before upgrading

**Run the migrations.** Three new tables and columns: `task_runs.saida`, the
`alertas` outbox, and `task_runs.map_index`.

```bash
brevis migrate up
```

**Deploy `brevis alert`, or failures stop being announced.** The scheduler no
longer talks to Slack: it records the alert in the same transaction as the
failure, and a third process delivers it. Without that process the alerts are
recorded correctly and delivered never — the outbox looks healthy and the
channel is silent. `deployments/kubernetes/alert.yaml` is the manifest, and the
scheduler says so at boot.

**A new port is bound.** `BREVIS_METRICS_ADDR` defaults to `:9090` on both
processes. Set it to `""` to serve nothing.

**Three behaviour changes to workflows that already exist**, all deliberate and
all matching Airflow:

| | before | now |
|---|---|---|
| a failure | stopped the whole graph | stops the branch below it; an unrelated branch keeps going |
| a retry | re-ran every step | re-runs only what failed |
| a step below a failure | had no record at all | is recorded `skipped`, saying which step stopped it |

The first is the one to weigh: an unrelated branch now writes its data on a run
that failed elsewhere. What the old abort protected against is covered by the
run still failing, the graph naming which step stopped each skipped one, and the
alert still going out — but it is a change, and
[the workflows doc](site/content/en/docs/04-workflows.md) states the trade.

### Fixed: the graph has had no chips, no badges and no phases since 0.7.0

`0.7.0` announced runtime chips, and they never rendered. The module rename from
`bravis` to `brevis` changed `internal/api/graph.go` and did not change
`web/assets/dag.js`, so React Flow fell back to its default node for a type it
did not recognise — which draws a plausible box with the step's name and none of
the rest.

Nothing failed, because nothing was checking. Every step on every graph has been
a bare box for three releases' worth of work: the runtime chips, the SDK badge,
the SDK phases, the context counts. A test now reads the island's source and
asserts every type the API emits is registered there.

The same class of bug produced two more, both found by looking at a screenshot
rather than at a test: a step's phases were positioned at a constant offset that
stopped matching the card once it grew rows, and the phase pills declared their
width in the island while the card declared it in the API. Every measurement
comes from the API now, and the island fills it.

### Added: flow shapes

```yaml
  - id: notify_failure
    depends_on: [extract]
    when: any_failed             # runs precisely when extract did not make it

  - id: load
    for_each: extract.partitions # one node, N instances, [3] on the card

  - id: transform
    unless_empty: extract.has_rows

  - id: mlops
    uses: ml_training            # another workflow, expanded at publish

  - id: start
    marker: true                 # a step with no command
```

Plus `group:` for a collapsible box on the graph, and labels on `depends_on`
entries. `skipped` is a state of its own — not a failure, not a success, in a
colour of its own.

`unless_empty:` is a **key**, not an expression: the step decides and publishes a
boolean. `uses:` is expanded at publish and not run as a child, which is the
design that deadlocked Airflow — the pod ceiling is a per-process semaphore, so
a parent holding a slot while waiting for a child that needs slots from the same
pool hangs, and only under load.

### Added: alerts that survive a Slack outage

The dispatcher used to call Slack and, on failure, log and carry on. That is
right for a pipeline and it is also how the alert was **lost**: no retry, no
record, nothing on a screen to say anybody should have been told.

It is an outbox now, written in the same transaction as the failure, drained by
`brevis alert` with backoff and a visibility timeout. An alert it gives up on is
**kept** — "raised, not delivered, 4 attempts, 403 from Slack" is the row that
matters — and the run's screen shows it.

Per-step alerting with `on_error: {type: SLACK}`. The destination is never in
the YAML: it is a credential, and the installation owns it.

### Added: metrics, and the weekly report

`/metrics` on a port of its own from both processes, in Prometheus format.
Queue depth, claim latency, slots, orphans recovered, run and step durations,
step attempts. No `run_id` label, asserted by a test.

`brevis report --window 168h` sends the periodic summary from a CronJob, with
`--dry-run` to read what it would have said. It carries no CPU or memory and
prints no zero for them: the engine does not collect those, and the message says
where they live.

### Added: context between steps

`BREVIS_INPUT` / `BREVIS_OUTPUT`, with libraries in Go (`sdk/context`) and
Python (`brevis` on PyPI) and nothing needed in bash beyond `jq`. Visible on the
run's screen, recorded in the database, and readable by a resumed run.

---

## [0.7.0] — 2026-09-07

### Added: the graph says what each step runs in

A step was a grey box with a command on it. Two identical-looking boxes can be a
Go binary in a 64Mi distroless image and a dbt project in a 1Gi Python one, and
the screen said nothing about the difference.

Each node now carries a runtime chip (Python, Go, Node.js, Java, Rust, PHP,
Ruby, .NET, SQL, Shell) and tool chips (dbt, Spark, Airbyte, Soda, SQLMesh,
Meltano, DuckDB, Airflow, Terraform).

**Nothing to write.** The engine reads `run:` and `image:` and works it out:

| the command | reads as |
|---|---|
| `python fetch.py` | Python |
| `cd /src && dbt build` | dbt |
| `spark-submit --py-files a.zip job.py` | Python + Spark |
| `cp in.csv /tmp/ && python x.py` | Python — **not** Shell |
| `dbt build` | dbt — **not** Python |
| `/opt/brevis/bin/fetch-weather` | **nothing** |

That last row is the one to read. A step the engine cannot make sense of gets
**no chip at all** — no empty row, no reserved space, nothing saying "unknown".
A card that says nothing is honest; a card that says the wrong thing costs
somebody the hour they spend believing it.

For the cases where the inference is blind — a bare binary, a wrapper script, an
image whose name says nothing — two optional fields per step:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go
  - id: transform
    run: python job.py
    tools: [spark]
```

An id outside the vocabulary is refused by `brevis publish`, naming what is
valid. Declared beats inferred, and **the screen shows which one it got**: an
inferred chip has a dashed border and a title saying so. That distinction is the
whole point — the `SDK` badge beside it is trustworthy because it is OBSERVED
and cannot lie, and a chip that hid whether it was a fact or a guess would
borrow that credibility.

Full documentation in [`docs/RUNTIME.md`](docs/RUNTIME.md).

### Fixed: signing in sent you to `/` instead of where you were going

Following a deep link while logged out bounced you to the login screen, and
signing in landed you on the dashboard. The filter, the page and the workflow
you were looking at were gone, with nothing saying why.

The redirect wrote `?de=` and the login screen read `?next=`. The parameter had
been renamed on the form and on the handler, and the redirect that writes it —
in another package — was missed. The value is also percent-encoded now, which it
was not: a destination carrying its own query string lost everything after the
first `&`.

A bookmarked `/login?de=/runs` no longer carries its destination. Bookmark the
destination itself.

### BREAKING for anyone matching on log text: every message is English

The whole engine now speaks English — every error, every log line, every string
on the screen:

```
before: step "run": saiu com codigo 2
now:    step "run": exited with code 2

before: morto por SIGKILL — normalmente falta de memoria
now:    killed by SIGKILL -- usually out of memory

before: execucao orfa: nenhum worker deu sinal em 5m
now:    orphaned run: no worker reported in for 5m
```

**If you have an alert, a dashboard or a log filter matching Portuguese text, it
stops matching.** Nothing else about it changed: same fields, same levels, same
moments.

### Fixed: `brand.yaml` named a field the file does not contain

An invalid colour was reported as `colour falha`, and the file says `failed:`.
It now names the YAML key, so whoever is fixing it looks for something that
exists.

### Fixed: `Validate` ran the same two checks twice

`MaxActive < 0` and the resource-quantity validation, over the workflow and
every node, appeared twice in one function. The second copy could never report
anything the first had not.

### No migration

`migrations/` changed only its comments. An upgrade from `0.6.0` is the image
and nothing else — unlike `0.4.0`, which needed `00007` before it would start.

---

## [0.6.0] — 2026-09-06

### Added: one box per pipeline element

SDK `v0.51.0` announces one phase for the source, one for each stage in the order
it runs, and one for the target. This engine accepts them, keyed by **position** —
two `Map`s share a name, and keying by name made the second overwrite the first.

Each box shows what it is (`from.HTTP …`, `to.Files …`) and what it did.

A fetcher up to `v0.50.0` still draws: with no `index`, the box is identified by
its name, the way it always was.

**Bring this engine up before the fetchers.** A `0.5.0` engine with a `0.51.0`
fetcher ignores the `map` and `aggregate` phases, and the screen goes back to
showing only `extract` and `load`.

---

## [0.5.0] — 2026-09-06

### Fixed: `panic: send on closed channel` in the Kubernetes executor

`seguirLogs` wrote to the same channel `Execute` closes on finishing, and nobody
waited for it. In production that takes **the whole process** down, not just the
run — and the process is the API or the scheduler.

Found by `-race` the first time the root module was tested in CI. Which brings us
to:

### The engine had no CI at all

Every job in `test.yml` and `quality.yml` did a `cd sdk`. The only
`go test ./...` at the root lived in the release gate — and since the release had
never run, the runner, the executors, the API and the scheduler reached `0.4.0`
without a test having run outside the machine of whoever wrote them.

There is now an `Engine` job: gofmt, build, vet, `go test -race`, `go mod tidy`,
generated artifacts, binary weight and lint. The module was dirty — eleven lint
problems, because it had never been linted.

### Added: the stages protocol accepts both formats

The SDK up to `v0.47.0` spoke Portuguese (`{"tipo":"etapa","nome","estado"}`);
from `v0.48.0` on it speaks English (`{"type":"stage","name","state"}`). This
engine understands **both**.

The bridge goes away when no fetcher below `v0.48.0` is left in production.
Without it, bringing the new SDK up would make the stages vanish from the screen —
no error, no log, just the grey box back.

### Added: an end-to-end test with a real SDK binary

It compiles a fetcher against the SDK, runs it through the process executor and
checks the stages in Postgres. Before this, everything on that path was tested
with a fake executor: the `@brevis:` line had never crossed an operating-system
pipe.

### Fixed: the Slack alert did not say the logical date's timezone

`Local()` is the timezone of whoever formats: the same event became `01:00` on the
developer's machine and `04:00` in the pod. The message now says which one it was.

---

## [0.4.0] — 2026-09-05

**The first published image.** Until here the engine existed only as code: there
was no `v*` tag, and therefore no image — for the reason in the next paragraph.

### Fixed: the image build was broken

The `Dockerfile` compiled with `golang:1.25` and the `go.mod` requires
`go 1.27.0`. Go's official image pins `GOTOOLCHAIN=local`, so it does **not**
download the missing toolchain: the build died in `go mod download` with

```
go: go.mod requires go >= 1.27.0 (running go 1.25.14; GOTOOLCHAIN=local)
```

This is what was blocking every release. It only shows up when somebody actually
tries to publish, because no other CI gate uses the Dockerfile.

### Breaking change: `BRAVIS_` became `BREVIS_`

**Every** environment variable changed prefix:

```
BRAVIS_DATABASE_URL  ->  BREVIS_DATABASE_URL
BRAVIS_HTTP_ADDR     ->  BREVIS_HTTP_ADDR
BRAVIS_ENV           ->  BREVIS_ENV
BRAVIS_LOG_LEVEL     ->  BREVIS_LOG_LEVEL
BRAVIS_BRAND_FILE    ->  BREVIS_BRAND_FILE
BRAVIS_TASK_ENV      ->  BREVIS_TASK_ENV
```

Anyone coming up from an earlier deployment has to rename them **first**: with no
`BREVIS_DATABASE_URL` the process does not find the database.

### Before bringing it up: run the migrations

`00007` adds two columns to `task_runs` (`etapas`, `sdk_versao`). The new code
`SELECT`s them, so bringing the image up without migrating leaves the run screen
in error.

### Added: the SDK's stages on the screen

An SDK step was a grey box that turned green. Between "started" and "finished"
there were forty minutes in which the screen could not tell "downloading page 300
of 4,803" from "stuck on the Redshift handshake".

It now appears as a group, with the stages inside — `check`, `extract`,
`transform`, `load` — each with a state, a duration and the number it produced.
Plus an `SDK v0.45.0` badge saying which version it was built with.

The transport is the log the engine already follows live: no new port, no new
permission. Since what recognizes the marker is the runner, the **local** executor
shows the same.

A step that is not an SDK one stays exactly as it was.

### Added: `env:` and `secrets:` per step

A step can declare the variables it needs, and a cluster secret is mounted by
name:

```yaml
nodes:
  - id: fetch_occurrences
    run: ./fetch
    env:
      WINDOW_DAYS: "7"
    secrets:
      - GABRIEL_SESSION_COOKIE
```

**What may be mounted is the installation's decision**, not the YAML's:
`BREVIS_POD_ALLOWED_SECRETS` lists the permitted secrets. Without it, no secret is
mounted — a workflow should not get to reach a secret just by naming it.

### Added: the rotated credential survives the pod

Per-step volumes, so that a credential renewed during the run does not die with
the container that renewed it.
