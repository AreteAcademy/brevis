# Changelog — the engine

The engine's versions, published as a Docker image (`areteacademy/brevis`). The SDK
has its own, in [`CHANGELOG.md`](CHANGELOG.md): they are two artifacts with
different audiences — one is a Go module somebody imports, the other an image
somebody operates — and that is why there are two lists.

**The namespace moved on 2026-09-13.** `0.13.0` is the first version published as
`areteacademy/brevis`; it also exists under the old name. **Every version up to
`0.12.0` is only on `daniel3843/brevis`** — the tags below are not a promise that
the new namespace has them.

The engine's tag is `vX.Y.Z`, with no prefix; the SDK's carries `sdk/`.

---

## [0.37.0] — 2026-10-26

### Fixed: SIGTERM drains an agent's running steps instead of killing them (#136)

`brevis-agent` logged *"running steps keep their streams until they end"* and
exited at once, taking every running `host:` step with it: `main` returned as
soon as the server stopped, while the goroutine meant to wait was still waiting.
A rolling restart of the agents failed the attempts it caught.

On SIGTERM an agent now **drains**. It takes no new step — a start is answered
`503`, and `/health` turns `503 draining` — while the running ones go on, their
resumes and cancels included: an engine restarted in the same rollout comes back
for the step it handed over and finds it. The agent leaves when each step has
ended **and its exit has been read**; a step that ended while the engine was away
would otherwise be answered "unknown execution" and run again. Then it exits `0`.

`--drain-timeout` bounds it (default `0`: no limit, the orchestrator's grace
period being the bound). Past it, or on a second SIGTERM, what is left is
cancelled — the whole process group — and the agent exits `1`.

### Fixed: a draining agent pod keeps its DNS name and its time (#136)

Kubernetes drops a terminating pod from a headless Service's DNS, so the
engine's resume of a step on a draining agent failed with `no such host`. The
agent's Service now sets `publishNotReadyAddresses: true`, and its StatefulSet
`terminationGracePeriodSeconds: 3600` — 30s would cut any real drain. In the
chart: `agent.terminationGracePeriodSeconds`.

### Changed: a start that ran nothing is sent again (#136)

With not-ready pods published, a start can land on an agent that is draining or
not yet listening. The engine now sends a start answered `503`, or refused at
connect, again — five tries over about fifteen seconds. It cannot run a step
twice: a start that did arrive makes the next one "already running" (#130).
Resumes and cancels are unchanged. An agent that is really down now fails the
attempt after those fifteen seconds rather than at once.

### Upgrading

No migration and no protocol change. **Apply the manifests** — or upgrade the
chart — with the agents: without `publishNotReadyAddresses` and the grace
period, the agent drains and Kubernetes still cuts it. Set
`terminationGracePeriodSeconds` (systemd: `TimeoutStopSec`) to the longest step
a host runs. See *When the agent stops* in `docs/KUBERNETES.md`.

---

## [0.36.1] — 2026-10-23

### Fixed: two starts of one execution id at once no longer run twice (#130)

The agent checked an execution id, released its lock, set the step up and
spawned it, and only then recorded the id. Two starts of one id arriving
together both got through: twenty released at once ran the command twenty
times under one id, the entries overwriting each other and all but one beyond
the reach of any resume or cancel.

Since 0.36.0 two runs never share an id, so this was reached by **one start
sent twice** — an engine or a proxy retrying a `Start` whose response was lost.
The id is now reserved under the same lock as the check, before any setup: a
second start is refused while the first is still being set up, and a resume or
a cancel arriving meanwhile finds it. A step refused during setup (a secret
outside the allowlist) leaves its id free.

Only the agent changed. No migration, no configuration, no protocol change:
upgrade the agents (`areteacademy/brevis:0.36.1-agent`); the engine images are
rebuilt at the same version for consistency.

---

## [0.36.0] — 2026-10-23

A run is executed once, a scheduler that stops leaves nothing it started
half-recorded or orphaned, and a `host:` step survives a scheduler restart.
**Three migrations and three upgrade rules — read "Upgrading" first.**

### Fixed: a run longer than 15 minutes ran twice (#121)

A run that took longer than the dispatcher's visibility window (15 minutes) was
requeued by the **same** scheduler still running it, and executed twice,
concurrently. Over seven days in two installations the second attempt ended as:

| | `success -> success` | `failed -> success` | `queued -> success` |
|---|---|---|---|
| installation A | 13 | 2 | 1 |
| installation B | 11 | 3 | 5 |

— every one a step doing its work twice: a fetch landing its records twice, two
dbt MERGEs racing on one `__dbt_tmp`.

- **A live holder renews its claim** every 5 minutes (`Visibility/3`) for as long
  as the run goes on. The orphan sweep reclaims only a holder **silent** for 15
  minutes — a worker that died — so a run may now take any time at all.
- **An attempt whose claim was taken anyway** (the database unreachable for longer
  than the window) is stopped — its pod deleted, its process killed, its `host:`
  step cancelled on the agent — and writes nothing. The run belongs to whoever
  holds it now.
- **Each scheduler claims under its own name**, `<hostname>-<pid>` (the pod's, in
  Kubernetes), carried on every dispatcher log line as `worker=`. All claimed as
  `local` before.

Runs already duplicated are not repaired.

### Fixed: a SIGTERM mid-run no longer records the run as a success (#122)

A deploy or a node drain sent the scheduler SIGTERM, and a run in flight was about
to be marked **success**: a cancel between two levels returned "nothing failed",
and a cancelled `host:` step — whose executor closes with no verdict — counted as
done. It did not land on 0.35.0 only because the process closed its database pool
before the dispatcher had written anything, which also meant no shutdown ever
recorded an interrupted run.

- **A step succeeded only if its executor said so.** An interrupted step ends
  `canceled`; the steps not yet started get no row.
- **The run is requeued for free**: `failed → retrying → queued`, no attempt spent,
  no alert, no backoff, its error saying the scheduler shut down. The next
  scheduler takes it at once instead of the sweep finding it fifteen minutes later.
- **`brevis scheduler` waits for its dispatcher** before closing the pool.
- A run whose graph finished as the signal came keeps its outcome.

### Added: a scheduler restart no longer restarts a `host:` step (#125)

On a SIGTERM a `host:` step in flight **goes on running on its agent**, and the
next scheduler **follows** it: the same execution, as the same attempt, its log
read again from the start of the agent's ring, ending as it ends. A forty-minute
load no longer starts over because the scheduler was redeployed.

When it cannot be followed, by why:

- the agent has no execution by that id (it restarted, or `--keep-finished`
  passed) → the step **starts again**;
- the agent's ring no longer reaches the last line read (more than `--ring` lines
  written meanwhile) → it is **stopped** and the attempt fails; the retry runs it
  under a new id;
- the agent cannot be reached → the attempt fails.

A handover that could not be written down is stopped instead. A **lost claim**
(#121) always stops the step. `BREVIS_HOST_HANDOVER`: `on` (default) or `off`,
which restores cancelling; anything else refuses to start. Pods and local
processes are not handed over: they stop with the scheduler, as above.

### Fixed: two runs of one workflow collided on an agent (#127)

The execution id was `<workflow>:<node>`, so the agent refused the second of two
concurrent runs of a workflow with a `host:` step — *"already running here"* — and
it failed at once. It is now `<workflow>:<node>[-i]:<run>:r<run attempt>:a<step
attempt>`. Two faces of the same bug on Kubernetes:

- **the instances of a `for_each:` step shared one pod**, the others adopting the
  first's and reporting its exit code as theirs;
- **a retried run adopted the previous attempt's pods**: the run's attempt never
  reached the pod name.

An unmapped step's pod name at run attempt 0 is unchanged.

### Fixed: a step that finished as its connection dropped is no longer failed (#128)

The agent forgot an execution *before* writing its exit line, so a connection that
dropped at the very end could not be resumed, and the engine failed the step — **a
step that had succeeded included**. The agent now keeps a finished execution — the
end of its log, its published context, its exit code — for **`--keep-finished`**
(`1h`), at most **`--keep-finished-max`** (`200`), the oldest first, in memory.
Cancelling a finished execution is a no-op (it used to answer "the process may
still be running"); a second start of a finished id runs again, as engines before
this one need.

### Changed: the console's reads by workflow use an index (#109)

`runs_workflow_criado_idx (workflow_slug, criado_em DESC)`, and the Workflows
page's strip of recent runs reads it one workflow at a time. On 100k runs over 50
workflows: the Workflows list **537 → 13 ms**, the recent-runs strip **~157 → ~1–3
ms**.

### Changed

- **A cancelled run stops what it started.** A `host:` step is cancelled on its
  agent when its run's context ends (it used to run on), and a cancelled pod is
  deleted even with `BREVIS_POD_KEEP_ON_FAILURE`, which keeps pods that *finished*
  failing.
- **The agent protocol, compatibly:** a resume of an execution the agent does not
  have answers **404** instead of a generic 400. A new engine reads an old agent's
  400 as "unreachable" — the safe side; an old engine reads a 404 as the error it
  always got.
- **Two scheduler log lines are in English** (#131): `orphaned runs recovered` and
  `runs created`, each with `count=` (were `runs orfas recuperadas`, `runs
  criados`, `quantidade=`). A log query or alert on the old text has to follow.

### Upgrading

1. **Migrate first**: `brevis migrate up` applies `00023` (`queue_items.renovado_em`),
   `00024` (the index, built `CONCURRENTLY` — writes go on, a large `runs` takes a
   while; the first migration outside a transaction) and `00025`
   (`task_runs.handover`), **before** the new scheduler and API serve. The new
   recent-runs query needs the index: without it, it is slower than before.
2. **Replace every scheduler replica, not some of them.** An old replica neither
   renews nor reads renewals — its sweep still measures a claim's age — so it would
   go on taking long runs from the new ones.
3. **Upgrade the agents too.** An old agent keeps nothing after a step ends and
   answers a lost execution with a plain 400: a handed-over step is then not
   followed — the attempt fails and the retry runs it again. Safe, not followed. An
   agent now holds up to 200 finished rings; lower `--keep-finished-max` on a small
   host.
4. **A shutdown needs its grace period.** Settling takes up to 15s, a `host:` cancel
   up to 10s, a pod deletion up to 30s; Kubernetes' default is 30s and Compose's
   10s, and neither is raised. A process killed first leaves its runs to the sweep.
   See `docs/KUBERNETES.md`, "When it stops".

No new required configuration.

---

## [0.35.0] — 2026-10-17

### Changed: the Overview (#117)

- **A window of 24 hours, 7 days or 30 days**, from three links at the top
  (`?window=`). The chart is per hour over a day and per day over a week or a
  month; a day's bar opens that day's runs.
- **Runs that needed a retry** are counted: a failure that healed, which does
  not show as one.
- **The next trigger** is in the strip; the eight soonest sit beside the chart.
- The head says what runs, what retries and what waits in the queue, each a link
  to its list. "In progress" shows only when something does.
- The donut goes; its four counts are in the strip.

No migration. No configuration changes.

---

## [0.34.0] — 2026-10-17

### Changed: a workflow's page (#116)

- **Pause and Resume** sit beside Run on a workflow with a schedule, one click,
  as on the Workflows list.
- **When it runs next** is a number in the strip: "in 12m" and the UTC time,
  "paused" or "manual".
- **A one-step workflow shows its step**, not a canvas holding one box: its
  command and what it runs in, beside its year of runs.
- The head says, in one line, whether the schedule runs, the project, the cron,
  the size of the graph and the tags.
- The workflow's runs no longer repeat its name on every row.

No migration. No configuration changes.

---

## [0.33.0] — 2026-10-17

### Changed: a load's page (#115)

A load's page answers whether a destination is being fed and how it is
configured, now in the first screen.

- **The configuration is in tabs**: Summary, Sources, Transform, Destination
  and Schema. A `from.Many`'s settings that every source shares are said once
  ("Common to 10"), with a table of only what differs between them. A link to
  `#sources` opens on that tab.
- **Rows read beside rows written.** The newest load says "0 of 370 read" when
  its run's stages know, in red when it wrote nothing of what it read. A
  mapped step, whose stages belong to one instance, shows no count.
- **The preview hides the columns that are empty** in every row it returned,
  with a switch to show them and a note naming them. It still loads only when
  asked. The SDK's own columns come last.
- The recent loads sit beside the page; the head says how the load stands,
  what it is, who writes it and when.
- **Freshness is a dot and a word** on every page, like a run's state.

No migration. No configuration changes. A pipeline describes its
configuration from SDK v0.85.0; a load written by an older one shows none.

---

## [0.32.1] — 2026-10-16

**`0.32.0` was tagged and never published:** its release stopped before any
image was built, on a manifest that pinned the gateway at the engine's version.
`0.32.1` is the same engine with that pin back at the gateway's own.

### Changed: the run page (#114)

The run page is where an operator lands from an alert, to find out whether it
worked, where it stopped, and how to run it again.

- **Rerun**, beside the title. A workflow with no params reruns in one click;
  one with params opens the Run… drawer filled in with this run's values. A
  param the workflow no longer declares is named and not sent. It is a manual
  run: for a run with a slot, the page says the rerun reads now's window, not
  the slot's.
- **A one-step workflow shows its step, not a canvas.** The step's command,
  runtime, SDK and duration on one line, and each SDK stage (check, source,
  map, target) with the numbers the graph showed. It follows a live run.
- **Step output is a table**: one row per attempt, opening to its error and
  log; a mapped step opens to its instances.
- **Alerts** are one line each, the error opening under its line.
- The top bar names the run (`daily_sales · 2c7233f8`), and its id copies.

No migration. No configuration changes.

---

## [0.31.0] — 2026-10-16

### Changed: a denser console (#113)

The console spent its space on ceremony -- large numbers, caps eyebrows, tall
table rows, full timestamps in every cell. The shared components now take the
compact measures, and every screen shows more with nothing removed.

- **Tables** have 34px rows and a 30px sentence-case header. Dates in a table
  read `Oct 16 02:23` (UTC), with the full date on hover. The Runs list shows
  about 20 runs in a 1440×900 window, up from 12.
- **One panel** heads every block (title, count, actions), replacing the caps
  section titles. The page title is smaller and the padding tighter.
- **A state is a dot and a word**, not a badge. A retried run shows `↻N`.
- **Key numbers sit in one strip**, the Overview's four cards included.
- **Small charts are smaller**: the load bars are 44px tall, and the year of
  runs uses 8px cells.
- **The run page**: the phases are a timeline, the error is a line you open
  for the full text, and the auto params are a key-value grid.

No migration. No configuration changes.

---

## [0.30.0] — 2026-10-16

### Fixed: no flicker between screens on real infrastructure (#112)

On a deployed console the screens took a moment to change and at times
flickered; the sidebar's search, especially, disappeared and reappeared.

- **Nothing pops in after the first paint.** Controls that need a script --
  the search, Copy, Live, tab bars, group toggles, the SQL workbench's strip
  and recent rail -- were drawn hidden and revealed by the deferred script, so
  a paint before it drew the page without them. One inline line at the top of
  the head now marks the page before anything is drawn, and they are there
  from the first frame. A stream's `#deadletter` link opens on Dead letter.
- **A page the pointer rests on is fetched before the click** (speculation
  rules, prefetch only). Never sign-in or sign-out, the API, the assets, or a
  Preview link -- that one is a billed warehouse query.
- **A click is acknowledged**: a thin bar at the top when the next page takes
  more than 120 ms.
- **The swap fades through**: the old page leaves, then the new one arrives,
  never both half-visible at once. The sidebar and the top bar hold still.
- **The graph comes with the page** instead of a round trip later, so the
  workflow and run pages no longer draw an empty canvas first.

No migration. No configuration changes. If the screens still feel slow on a
deployment, check that the proxy keeps `Cache-Control: immutable` on
`/assets/*?v=` -- the console sends it, and a proxy that rewrites it costs a
round trip per asset on every screen.

---

## [0.29.0] — 2026-10-15

### Changed: Catalog becomes Streams and Loads (#111)

Catalog listed two mechanisms as "destinations", and a stream's dead letter as
a destination of its own. The console now has an **Ingest** group with two
screens, each answering one question:

- **Streams** (`/ingest/streams`): what comes in through the gateways. One card
  per stream, `POST <path>`, with its compact flow and its dead letter hanging
  off the destination ("refused batches"). A stream's page has Overview,
  Configuration (`#config`, everything it declares, "—" for what it does not)
  and Dead letter (`#deadletter`: the path, the gateway metric that counts what
  it buried, the ls/resend commands), plus **Send test event**, a drawer with
  a validated body and a curl to copy. The console sends nothing.
- **Loads** (`/ingest/loads`): what the workflows wrote, grouped by workflow,
  with freshness, last load and rows. A load's page is one step on one
  destination: rows per load (each bar opens its run; zero is red), recent
  loads, configuration, Preview and Query in SQL.

The sidebar is now Pipelines · Ingest (Streams, Loads) · Consume (Endpoints,
SQL) · Workspace, and ⌘K lists streams and loads instead of destinations.

**Old links keep working.** `/data` answers 301 to `/ingest/loads`;
`/data/target?u=<uri>` answers 301 to the stream whose sink or dead letter the
URI is, else the load that writes it, else `/ingest/loads`. `/data/endpoints`
is unchanged.

No migration. No configuration changes. The streams list reads every stream's
configuration in one query (`gateway_streams` joined to `writer_configs`).

---

## [0.28.0] — 2026-10-15

### Changed: the Pipelines screens (#110)

**Workflows.** The search answers while you type: the server still searches
(the list is paginated), and the list is swapped under the box. State and
schedule are Segmented filters, each choice with its count. Tags are a popover
with a box that filters them, and two tags combine with AND (`?tag=a&tag=b`).
Each row draws its last 14 runs as squares linked to the runs, read in one
query for the page, and says its schedule in words for the shapes that are
certain (`0 4 * * *` → "every day at 04:00"); any other expression is shown
as written.

**Workflow.** The title is the slug, with its project, schedule in words and
Active/Paused above it, and the four numbers are one Stats strip. A workflow
with parameters runs from a "Run…" drawer with a preview of the payload; the
trigger is the same POST. With no script, the form shows in place.

**Runs.** The state filter counts each choice, each duration has a bar against
the page's longest, and a "Live" switch (off by default, remembered per
browser) refreshes the list every 5 s, washing the new runs. Nothing is
requested while the tab is hidden.

**Run.** A strip of phases under the header -- scheduled or triggered, how late
it started (from the slot, like `delay_seconds`), how long it ran, how it
ended -- refreshing with the rest of the live head.

No migration. No configuration changes. On a large history, `runs` would
benefit from an index on `(workflow_slug, criado_em)` (#109).

---

## [0.27.0] — 2026-10-15

### Added: a destination's page draws how data reaches it (#108)

Each described writer's configuration card on `/data/target` now opens with
its flow: the source (`POST <path>` and its format for a gateway stream, the
driver and what it reads for an SDK pipeline), the buffer when its flush is
configured, the destination, and the dead letter as a dashed branch -- where
refused batches go.

It draws only what the configuration declares. Every label is a configuration
field or the destination itself, and a writer that did not describe itself
(a Python, dbt or shell step) gets no diagram. Drawn on the server, it needs
no script, and below 700px the nodes stack.

The site's illustrations of the console now use the console's own palette.

No migration. No configuration changes.

---

## [0.26.0] — 2026-10-15

### Added: ⌘K goes anywhere in the console (#107)

Press ⌘K (Ctrl+K) on any signed-in page, or the sidebar's "Search or jump
to…", to search the sections and everything the console lists: workflows,
projects, destinations and endpoints. Pasting a run's id offers that run.
↑↓ choose, ↵ goes, ⌘↵ opens a new tab, and Esc closes the palette and
gives the focus back. It works from inside the SQL editor too.

It only navigates; nothing in it changes the system. Matching is a plain
substring, ranked by the label's start, then a word's start, then anywhere.
The index is `GET /api/palette` -- behind the session like every page,
`no-store` -- fetched once per page and filtered in the browser.

No migration. No configuration changes.

---

## [0.25.0] — 2026-10-15

### Changed: the console is redrawn, and stops blanking on every click (#105)

The console keeps its identity (IBM Plex, the brevis.sh palette, dark) and
its shape -- server-rendered pages with small islands, no JavaScript build --
and changes how it is laid out and how it moves.

- **A sidebar** replaces the top bar: Pipelines (Overview, Workflows, Runs),
  Data (Catalog, Endpoints, SQL), Workspace (Projects), and the account at its
  foot. The top bar carries a breadcrumb. Below 960px the sidebar is a drawer
  that opens with no script.
- **No flash.** Every navigation crossfades (`@view-transition`) and the
  sidebar holds still. A SQL run swaps only the answer pane, without
  reloading the editor. After 120ms a run shows "Running" with a timer and a
  Cancel that stops the query in the warehouse.
- **Assets are cached by version.** Every page asks for `/assets/…?v=<digest>`,
  answered `immutable`, so a remote console no longer revalidates its
  stylesheet, scripts and fonts on every click. A changed file gets a new URL.
- **Catalog** is a dense list searched by URI and writer, on the server and
  while typing. A destination's page is titled by its URI, in two columns.
- **Endpoints** is a searchable index. An endpoint's page reads as its
  contract, its Try it swaps only its answer, and its curl follows the form.
- **SQL**: "Writes allowed" in the top bar on a writable connection, every
  token painted from the palette (NULL, TRUE and type names were nearly
  invisible), CSV export of an answer, and on a phone the workbench stacks.
- A component gallery at `/_components`, served only with `BREVIS_ENV=local`.

### Fixed

- Every workflow and run page threw `rf is not defined` in the graph's init
  (#106). Nothing visible depended on it; the error is gone.
- "Reads only" was drawn under the editor on a writable connection too.
- A destination's rows-per-load bars had no height and grew to a third of the
  screen.

No migration. No configuration changes.

---

## [0.24.0] — 2026-10-14

### Added: a destination's page says how to get its dead letter back (#86)

A gateway stream's configuration on `/data/target` gains a **Dead letter**
section: where the refused batches go, and the two commands that list them and
send one back -- `gateway deadletter ls` and `resend`, from gateway 0.31.0 --
with the stream's name filled in, and what a resend does under `write: append`,
`merge`, or a destination with no merge at all (Pub/Sub).

The console runs neither and reads nothing of the dead letter: it holds no
credential for it and no driver, and the gateway still shares nothing with the
engine at runtime. The commands run where the destination's credentials are.
`<gateway.yaml>` is a placeholder, since the manifest does not carry where the
config sits in the gateway's pod. A dead letter that is not `files` gets the
reason instead of a command.

---

## [0.23.0] — 2026-10-14

### Added: a queued run says what concurrency limit holds it (#95)

A run waiting on its concurrency group, or on `concurrency: N`, looked like any
other queued run -- including the manual run that #32's group now makes wait
behind the workflow it shares models with. Its page now says, while it waits:

> Waiting for concurrency group `jaffle_models` (limit 1) — held by `every_15`
> since 2026-10-13 10:15:00 UTC

with a link to each run in a slot (three, then a count), and how many runs of
the same group are ahead of it when the slot is free but others come first. The
line arrives and leaves with the page's live refresh.

It repeats the claim's arithmetic, and draws nothing when the limit is not what
holds the run: a run waiting on a dispatcher slot, `--max-pods` or a retry's
backoff gets no reason rather than a wrong one.

---

## [0.22.0] — 2026-10-13

### Added: a concurrency group, so two workflows stop overlapping (#32)

`concurrency: 1` limits runs of ONE workflow, and nothing could say that two
must not run together. A daily and a `*/15` building the same dbt models did,
when a manual run of the daily landed on a slot of the other: one dropped the
`__dbt_tmp` table the other was reading. Both now name the shared resource:

```yaml
concurrency:
  group: jaffle_models
  limit: 1
```

- Runs of every workflow in the group count against one limit — scheduled,
  landed, backfilled and manual. A run that finds the group full waits in the
  queue. The claim counts by `COALESCE('g:' || group, 'w:' || slug)`, inside the
  same query as before; a workflow with no group behaves exactly as it did.
- `concurrency: N` is unchanged. A group without a limit means 1; `limit: 0`
  with a group, a group name that is not lower case, digits, `_` and `-`, and an
  unknown key inside `concurrency:` are refused.
- `validate` and `publish` refuse a group whose members declare different
  limits; `publish` also checks the members already published, except the ones
  it replaces or `--prune` removes.
- `validate` no longer ends every error of the set with "`uses` could not be
  expanded".

**Upgrading:** migration `00022` adds `runs.grupo_concorrencia` (nullable, no
backfill). Runs queued before it have no group and keep their per-workflow
limit.

---

## [0.21.0] — 2026-10-12

### Added: a destination's page says how each writer is configured

`/data/target` showed where a writer lands and how much. It now shows **how**:
a Configuration section per writer, in three layers.

- **A sentence**, for somebody who does not read YAML — *"JSON events posted to
  /v1/ingestion become asset_\* tables in BigQuery acme.bronze, appended so a
  redelivery is a second row, in batches every 1 minute or 1 event; batches
  the destination refuses go to files at ./dead-letter/."* Written from the
  settings by templates, never generated.
- **Every setting**, by section — Stream, Identity, Buffer, Sink, Retry, Dead
  letter for a gateway; Source, Transform, Target for an SDK step — with the
  defaults a gateway filled in marked, and the config file's comments beside
  the setting they were written on.
- **Changes**, newest first, setting by setting: a gateway's publishes; a
  step's runs, each version linked to the run that first wrote with it.

A gateway sends it in manifest version 2 (`brevis gateway publish` reads 1 and
2); a Go SDK step in a new `pipeline` marker. A step that sends none — Python,
dbt, a shell — gets a section saying so. See `docs/CATALOG.md`.

### Added: a configuration carrying a credential is refused, whole

Both writers keep credentials out by construction. The engine looks again: a
URL with a password, a key written as `key=value`, or a setting named like a
credential with a value refuses the whole description — a manifest fails to
publish naming the stream and the setting; a step's description is dropped
with one warning naming the run and the step, and its landings are recorded
without it. Blanking the password would hide the bug in the writer.

### Added: Data → Endpoints, and Try it

`brevis-sql-api` (brevis-sql `0.5.0`) serves declared, read-only SQL over
HTTP. Like a gateway it shares nothing with the engine at runtime, and appears
in the console through a deliberate act:

```bash
brevis-sql-api describe --out manifest.json api.yaml
brevis endpoints publish manifest.json     # and `unpublish <api>`
```

**Data → Endpoints** lists every published path; an endpoint's page is its
contract — parameters with what they accept and their defaults, limits, the
author's `#:` notes, the models it reads, a `curl` with the key as a
placeholder. A model's page gains **Read by**. No traffic anywhere: that is
the API's `/metrics`.

**Try it** runs an endpoint from its page and draws the answer — status, cache
result, time, the first 500 rows — calling the API from the server, so the
browser never holds a key. It runs with one key, `BREVIS_ENDPOINTS_KEY`, so
**one tenant for every login**: it exists only on a console that asks who you
are, and only for an API in `BREVIS_ENDPOINTS_URLS`.

The manifest is refused whole, by position, when any target is not one or any
name, default or note carries a credential's shape. See `docs/ENDPOINTS.md`.

### Changed: the bar follows the data's path

Overview · **Pipelines** (Workflows, Runs) · **Data** (Catalog, Endpoints, SQL)
· Projects. Each group is announced as one to a screen reader. Every URL is
unchanged.

### Upgrading

**Four migrations, all quick.** `00018` creates three tables. `00019` adds a
nullable `config_hash` to `landings`, with a foreign key: on a million landings
(391 MB) it took **20.6 ms** — Postgres validates a key on a column that is all
NULL without reading the table. `00020` creates `step_config_history`, filled
from the landings `00019` began describing — none, on an engine upgrading from
`0.20.0`. `00021` creates `api_endpoints`, empty until an API is published.

**Upgrade the engine before the gateway.** An engine on `0.20.0` refuses a
version-2 manifest by its number, and the publish fails saying so. This one
reads both.

**SDK steps need no order.** A newer SDK's `pipeline` line reaching this engine
before its upgrade is consumed as an unknown marker, and its landings carry no
configuration until it is.

**`deployments/kubernetes/gateway-publish-job.yaml`** is new: the gateway's
publish as part of its deploy. It pins this engine and the gateway release that
carries manifest version 2.

**Try it is off until configured.** Two optional variables:
`BREVIS_ENDPOINTS_URLS` (`api=url` pairs) and `BREVIS_ENDPOINTS_KEY`. A list
that cannot be read stops the engine at start, naming the entry.

**`deployments/kubernetes/endpoints-publish-job.yaml`** is new: an API's
publish as part of its deploy, pinning this engine and brevis-sql-api `0.5.0`.

---

## [0.20.0] — 2026-10-10

### Added: the workbench's tree tells a schema from a table at a glance

In `/sql`'s object tree a schema and a table were the same colour, the same
size and nearly the same indent. In a schema of forty tables the eye could not
find where one ended and the next began.

Each name now has a shape in front of it — a cylinder for a schema, a grid for
a table:

```
▾ bigquery://project
  ⛁ staging
     ▦ agents_model_prices
       + columns
  ⛁ silver
     ▦ billing_contract
       + columns
```

**A shape and not a colour.** A second colour would have to stay readable on
every installation's ground, and `brand.yaml` rewrites those tokens. The icon
is drawn in the row's own colour, so it follows a theme and a hover with
nothing new to keep in contrast. A screen reader still hears only the name.

A long name truncates and keeps its icon, and `+ columns` and the column list
start under the table's name, so they read as belonging to the row above.

### Upgrading

**Nothing to do.** No configuration, no change to `brevis-sql`, and no
migration — the schema is unchanged since 00017.

---

## [0.19.0] — 2026-10-10

### Added: a write answers with what it changed, instead of an empty grid

`brevis-sql serve` can now be told that a connection accepts statements which
change data. Drawn through the grid that answers a `SELECT`, a write came back
as a table with no headings and *"No rows."* under them — which reads as *"it
found nothing"* for a statement that was never looking.

The workbench says what happened instead:

```
12 rows changed · 0 B
0 rows changed · 0 B
ran — no rows to count · 0 B
```

**Three answers and not two.** "12 rows changed" and "0 rows changed" are one
sentence with a different number and both are a count; a `CREATE TABLE`
changed nothing and did not fail. Printing "0 rows" for it reads as a failure
to whoever ran it, and they run it again.

**The console still does not read the SQL.** It does not classify it, count
its statements or price it. It is told what happened by the only thing that
can know, and draws a sentence where the grid would be.

### Added: the workbench says when it may change the warehouse

A connection on which writes have been unlocked carries a banner above the
editor, naming nothing a script has to read it to see: *"Statements here can
change data on this connection — it was unlocked deliberately."*

It is drawn only where the service said so, per connection — one `serve`
reaches several warehouses and may have been unlocked for one of them. With
no unlocked connection nothing on any screen changes.

The reader this is for is not the one who types `DROP` on purpose. It is the
one who does not know this console can, because the last console they used
could not.

### Changed: the SQL deployment no longer needs a second service account

`deployments/kubernetes/sql-serve.yaml` says why: the pod that holds the
warehouse credential used to refuse any role that could write, which meant a
second account to create, keep and rotate beside the one `brevis-sql build`
already needs. The warehouse is asked to refuse now, so a read-only role is
still welcome and no longer required.

### Upgrading

**Both screens need `brevis-sql` 0.4.0 beside them.** The sentence and the
banner are drawn from fields an older service does not send, and an engine
pointed at one simply draws neither — no error, no empty promise. Nothing
else in this release depends on it.

**Nothing changes for a console with no SQL service.** `BREVIS_SQL_SERVE_URL`
unset is still no menu entry and no tabs.

**No migration.** The schema is unchanged since 00015.

---

## [0.18.0] — 2026-10-10

### Added: a workflow can start when something lands

```yaml
trigger:
  on_landed: [bigquery://acme-prod/bronze/orders]
  debounce: 5m
```

A workflow runs when a step declares it wrote one of the targets it names. A
dataset pattern is admitted — `bigquery://acme-prod/bronze/*` — because
`auto_table` creates a table per route, so a dataset is the only thing a
subscription can name for them.

**The debounce is the idempotency key, not a timer.** `runs.idempotency_key`
has been `TEXT NOT NULL UNIQUE` since 00002 and the scheduler composes it as
`slug:trigger:slot`. Make the slot the window a landing falls in, and ten
landings compose one key: the database refuses the other nine. No new state,
nothing to expire, and nothing to get wrong across a restart.

**The debounce needs its unit** — `5m`, not `5`. A bare number is refused at
`brevis validate`, which is the same code path `brevis publish` runs, so a
folder that validates is a folder that publishes.

A run started this way records the targets that started it, and the console
says which they were.

### Added: `/sql` — a workbench, not a text box

The console gets a three-zone screen: an explorer, an editor, and the answer.

- **One tree, with the connection as its root.** Every queryable destination
  the catalog knows is a node. Opening one fetches its schemas, opening a
  relation fetches its columns — and leaves them open. Three relations in
  three schemas stay open at once.
- **Nothing reloads.** Two fragment endpoints answer HTML rather than JSON, so
  the tree's classes, its ARIA and its "this warehouse does not say" have one
  definition instead of two. Without JavaScript the same buttons still submit
  the form, as they always did.
- **The editor completes names** from what the tree has loaded, with
  `Ctrl+Space` and on `.`. The screen says which half it knows: columns
  complete for relations you have opened, because fetching every relation's
  columns is the project-wide `COLUMNS` query this engine refuses.
- **The answer is a grid**, with a sticky heading, a row-number column that is
  not selectable with the data, `null` drawn as the word, numbers aligned by
  reading the cell rather than its Go type — BigQuery returns every scalar as
  a string — and a pager that costs no second query and prints `500+` rather
  than `500` when a limit cut the answer.
- **Several statements open at once**, with a Recent rail of the last twenty
  and what each cost. Both live in `localStorage`: the statements are on that
  machine, in that browser, and clearing site data empties them. No tab state
  reaches a URL, because a statement in a link is a statement in a proxy log,
  a browser history and a `Referer`.
- **The cost line says what a statement will process before Run is pressed**,
  and a statement over the service's ceiling is refused there with the
  sentence it would get at Run. It needs `brevis-sql serve` 0.3.0; without
  one, the line is silent.

**Every signed-in screen now uses the window.** `Wide` was a flag four screens
remembered and five forgot; full is the shape, and a narrow measure is
something a screen asks for around its own prose.

### Added: a destination shows its first rows, and the engine still has no driver

`/data`'s destination page gains **Preview** and **Query** tabs. The engine
holds no warehouse credential and gained no driver: it asks `brevis-sql serve`
and draws what comes back. A destination that is not a relation — a bucket, a
topic — is offered neither, because a tab that can only ever say no invites
somebody to try.

Both are opt-in: with `BREVIS_SQL_SERVE_URL` unset there is no SQL entry in
the menu and no tabs on a destination.

### Fixed: `?next=/\evil.com` was an open redirect

`escapeTarget` refused `//evil.com` and `https://evil.com` and let
`/\evil.com` through. It looks like a path and is not one: a backslash is a
slash in a URL, so a browser normalises that to `//evil.com` and the host is
evil.com. Measured with `url.Parse`, not reasoned about. What it buys is the
thing that function exists to stop — our own login screen handing the operator
who just authenticated to somebody else's page.

### Fixed: the data tools outlived the console's own login

The console can run with no credential and warns at boot: *"interface is OPEN:
anyone can trigger a workflow"*. A Query tab made that sentence incomplete —
it is now also *anyone can read every table in the project*, because a query
is deliberately not confined to the destination it was opened from. The tab
does not create that hole; it changes what falls through it. Preview and Query
are offered only on a console that asks who you are.

### Fixed: the workbench's POST never checked the catalog

`/sql`'s GET path consulted the catalog and its POST path did not, so a
crafted body reached the warehouse: measured, `[/v1/query /v1/objects]`. Two
copies of a rule are two rules, and the second one was missing for as long as
the first one existed. One function answers it for both doors and for the two
fragment endpoints.

### Fixed: the result grid was changing large numbers

`encoding/json` turns every JSON number into a `float64`, which has 53 bits of
mantissa, so `9007199254740993` was drawn as `9.007199254740992e+15` — a
different id, and in a notation no warehouse stores. Right on BigQuery, which
returns scalars as strings, and wrong on Postgres, which does not. The decoder
keeps the digits as text now.

### Fixed: `0 backfill run(s) queued` was four different answers

The number was always correct; the sentence never said how small out of what.
Zero answers "the interval is already backfilled", "the dates are wrong", "the
cron never fires in that range" and "the workflow is the wrong one"
identically. It now says which.

### Fixed: four defects in the workbench's own island

Each found by driving the script rather than reading it, and each invisible
on the screen:

- **The editor was drawn in CodeMirror's light default.** `app.css` paints it
  from this interface's tokens and the vendored sheet loaded *after*, with the
  same selectors and the same specificity — so every one of those rules was
  dead, on a console whose canvas is `#141711`. A vendored sheet is a default
  and loads first now.
- **The workbench never restored a drag or a collapsed rail.** The storage
  prefix was a `var` declared below the startup calls that read it; a `var` is
  hoisted without its value, so every key read at startup was `undefinedrail`
  while every key written during a drag was `brevis.workbench.rail`.
- **The query tabs threw on the first click** in every browser that loaded
  CodeMirror — the same hoisting mistake, one release later. All of the
  island's state is declared above the calls that fill it now, and a test
  refuses the shape.
- **A test that could not fail**, three times over: assertions about the
  island's code were being satisfied by the comments that explain it.

### Upgrading

**Run `brevis migrate up`.** Three migrations, all additive, all with a down
path, and none touching a table another one does not:

- **00015** adds `landings.recorded_at` and backfills it from `loaded_at`.
  Measured on the shape 00013 used — 350,400 landings, a year of hourly loads
  across forty workflows, 165 MB:

      ADD COLUMN NOT NULL DEFAULT now()       7.5 ms
      UPDATE ... SET recorded_at = loaded_at  7.4 s
      CREATE INDEX                            231 ms

  The `ADD COLUMN` does not rewrite the table; the `UPDATE` does, and leaves
  it at 269 MB until it is vacuumed. The index is built **last** because
  building it first made the same `UPDATE` take 10.8 s.
- **00016** creates `landing_cursor`, one row, and an index on the workflows
  that subscribe. **00017** adds `runs.trigger_targets`, nullable. Both
  instant.

Migrations run inside a transaction and `migrate up` gates the rest of the
stack, so the window is the sum above.

**Nothing fires on the first cycle.** The landing cursor is planted at `now()`
and never at the epoch, and only once something subscribes — otherwise the
first scheduler tick after the upgrade would start one run per debounce window
for the whole history of the `landings` table.

**Nothing changes for a console with no SQL service.** `BREVIS_SQL_SERVE_URL`
and `BREVIS_SQL_SERVE_TOKEN` are both optional and both unset by default; with
no URL there is no `/sql` entry, no Preview tab and no Query tab. The service
that answers them, `brevis-sql serve`, is a separate binary holding a
warehouse credential and is a decision of its own — see
[`sql/CHANGELOG.md`](sql/CHANGELOG.md) 0.3.0.

**Rolling back is an image, not a schema.** The new columns are additive and
every query in the Postgres layer names its columns, so `0.17.1` runs against
this schema. Do not run the down migrations to roll back.

---

## [0.17.1] — 2026-10-08

### Fixed: the `v0.17.0` tag deploys 0.16.1

`VERSION` said `0.17.0` and every manifest in the tag said `0.16.1`:

```
v0.17.0:deployments/kubernetes/api.yaml    image: areteacademy/brevis:0.16.1
v0.17.0:deployments/helm/brevis/Chart.yaml appVersion: "0.16.1"
```

A `kubectl apply` or a `helm install` from that tag brings up the previous
engine — without migrations 00013 and 00014, without `/data`, and without
`brevis gateway`, which is the entire release. **The published images were
always correct**: the Dockerfile builds from source and `VERSION` was right, so
`areteacademy/brevis:0.17.0` is the engine it claims to be. Only the files in
the tag are wrong.

`image-pins-check.sh` caught it. `Test & Lint` went red on the release commit,
the tag was cut anyway, and the fix landed thirty minutes later outside it.

**So `release.yml` runs that check itself now**, beside the one comparing
`VERSION` with the tag and before the first image is pushed. The gate existed
and the release did not have to pass it; it does now.

Nothing else changed. If you deploy from `master` or from images, you already
have `0.17.0` and this changes nothing for you.

---

## [0.17.0] — 2026-10-07

The catalog: what every pipeline, step and gateway writes, and which of it is
late.

### Added: `/data` in the console

Every destination the ecosystem writes, who writes it, when it last received
data, and whether that is **late against the writer's own schedule**: an
hourly table three hours old is stale, a daily one is fine. Statuses are on
time, late, stale, paused, no schedule, and — for gateways — continuous. Problems
first, filters in the URL, and a page per destination that says which slot a
writer missed and draws its last loads. See `docs/CATALOG.md`.

### Added: steps say which table they wrote

A step prints one line, and the engine keeps it in a new `landings` table:

```
@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/orders","rows":48213}
```

The Go SDK does it by itself from **sdk 0.81.0**; Python from **brevis 0.8.0**
(`landed()`); dbt and shell steps echo the line. See `docs/LANDINGS.md`. An
SDK older than 0.81.0 keeps working and lands nothing.

### Added: `brevis gateway publish | unpublish`

A gateway appears on `/data` from a published manifest — the output of
`gateway describe` (gateway 0.22.0) — never at runtime. See `docs/GATEWAY.md`.

### Upgrading

**Run `brevis migrate up`.** Two migrations:

- **00013** creates `landings` and backfills it from the phases already
  recorded, as legacy labels. Measured on a year of hourly runs across forty
  workflows (350,400 task runs, 492 MB): **about 11 seconds**, inside the
  migration's transaction, once. Plan the deploy window for it.
- **00014** creates `gateway_destinations`, empty until a gateway is published.

Neither touches an existing table. `landings` has no foreign key to `runs` and
`brevis prune` leaves it alone, like `load_metrics`.

---

## [0.16.1] — 2026-10-06

Three things that cannot change the behaviour of a workflow you already have.

### Added: the agent answers `GET /health`, and its Pod has a readiness probe

The engine reaches a pool of agents through a **headless Service**, whose DNS
already filters on Ready — and nothing was deciding Ready. A replica was Ready
the instant its container ran, before the listener bound, so a start during a
rolling restart could land on a connection refused.

```yaml
readinessProbe:
  httpGet: {path: /health, port: http}
  periodSeconds: 5
```

It is in `deployments/kubernetes/agent.yaml` and in the chart's agent template,
and it needs nothing from you.

The route **checks nothing**, deliberately. The agent has no dependency to be
ready for, so a probe that could fail for a second reason would take a pod out
of its Service for something other than "not serving". It is also the only
route that answers without the token — a kubelet sends no `Authorization`
header — and the exemption is one exact path, so `/healthz` is still refused
rather than answered with a 404 that tells an unauthenticated caller which
paths exist.

**Readiness and not liveness.** A liveness failure sends SIGTERM to a pod that
may be holding hours of a step's work, and the agent is built the other way
round: its own shutdown lets running steps finish their streams. Taking a
replica out of the Service is the strong action here; killing it is not the
probe's to take.

### Added: a field the decoder does not know is said out loud

```
$ brevis validate workflows/
  warn  workflows/etl.yaml: line 5: `hosts` is not a field of a step, and was ignored
  warn  workflows/etl.yaml: line 9: `depends-on` is not a field of a step, and was ignored
  ok    etl                          dag  2 steps, 0 dependencies  (manual)
```

The workflow decoder has never been strict, so a key it does not recognise is
dropped and nothing is printed. `hosts:` for `host:`, `depends-on` for
`depends_on` — the file publishes, the line does nothing, and the first symptom
is a graph with fewer edges than the author wrote. Look at the `ok` line above:
**0 dependencies**, because `depends_on` was the other typo.

It is the near half of the gap `0.16.0`'s own upgrade note names: an engine that
does not know a field DROPS it, so a step meant for the machine with the licence
on it runs in the engine's own container. Nothing here fixes that for an engine
older than this one — nothing can — and it is why the silence was worth
breaking.

**A warning and not a refusal.** Every workflow anybody has may carry a stray
key today; refusing would fail files that have worked for a year in order to
catch a typo in one of them. `validate`, `publish` and `run` all say it, on
stderr, and the engine behaves exactly as it did.

One limit, said rather than discovered: a `depends_on` entry decodes through its
own unmarshaller, which is where the strict pass stops looking, so a stray key
*inside* one is still dropped in silence.

### Fixed: the site said every step becomes a pod

`llms.txt` — the page written to be quoted — opened with "Every workflow step
runs as its own Kubernetes pod with its own image". True on 2026-10-05. The
step-fields table listed fifteen fields and not `host`, and the CLI page's
"three limits" of `brevis run` were four.

The fourth was measured rather than assumed: **`brevis run` does not read
`BREVIS_HOSTS`**, so a step with `host:` cannot run there even with the variable
set. The engine's refusal still says "they come from the installation, in
BREVIS_HOSTS", which is true of the scheduler and not of `run`. That message is
a known gap; the page says the truth either way.

---

## [0.16.0] — 2026-10-06

### Added: `host:` — a step runs on a machine the engine does not manage

```yaml
steps:
  - id: extract
    host: vendor-box
    run: vendor fetch --since {{ .date }}
```

A step already had three places to run: the engine's own process (`action:`), a
local one (`run:`), and a pod the engine creates (`image:`). `host:` is the
fourth and it is the inverse of the third — **the runtime is already up, and
Brevis did not create it.**

It exists for the two cases a pod per step answers badly. A vendor's extractor
with a licence pinned to a machine, and a dbt project whose image belongs to the
team that owns the models, not to the team that owns the engine. In both, the
version of the tool stops being coupled to the version of Brevis.

The other half is an agent, published for the first time with this release:

```
areteacademy/brevis:0.16.0-agent
```

16 MB, alpine rather than distroless — a runtime with no shell cannot run the
commands a step names. You build YOUR image on top of it:

```dockerfile
FROM areteacademy/brevis:0.16.0-agent
USER root
RUN apk add --no-cache python3 py3-pip && pip install --break-system-packages dbt-bigquery
USER brevis
```

The agent is the **floor** of that image and not a sidecar beside it: one
container, one process tree, your tools on the `PATH`. A step then says
`dbt build` with no path, no shared volume and no init container.

The engine finds it by name:

```bash
BREVIS_HOSTS=vendor-box=https://10.0.4.7:9443,dbt=http://dbt.data.svc:9443
BREVIS_HOST_TOKEN=...
```

A step naming a host this installation does not know is refused **by name**, and
the message lists the hosts that are configured — "unknown host" alone sends
somebody hunting for a typo that may be in the installation rather than in the
YAML. `host:` together with `image:` or `action:` is refused earlier, at
publish: those are alternatives, and a step that named both would otherwise sit
in a schedule and fail at three in the morning on whichever executor won.

**What the agent will not do** is as deliberate as what it does. It is not an
orchestrator: it knows nothing of workflows, dependencies, schedules or retries,
and an agent that learned any of it would be a second orchestrator to keep in
agreement with the first. It is not a secret store either — it RESOLVES a
coordinate the engine sends into a file in a directory THIS host allows, under
`--allow-secrets`. The engine never sends a value.

It is stateful per execution — the output ring and the process handle live in
that process's memory — so a pool behind one address has to say where to come
back to:

```
--advertise https://$(POD_NAME).dbt.data.svc:9443
```

Told rather than discovered: the process can read its hostname and cannot know
which of its names the engine routes to. Without it, a pool's symptom is a step
failing on the first network blip with its process still running. With it,
`replicas: 3` behind a headless Service is the whole of scaling, and the engine
comes back to the instance that took each step for a resume or a cancel.

**A token is required**, and running without one has to be said out loud:
`--insecure-no-token`. An agent with no token runs any command for anybody who
reaches the port, so the dangerous choice is a declaration rather than an
omission. There is no TLS of its own yet — put it behind something that
terminates TLS, or on a network where that is somebody else's job.

Ready to deploy, both ways:

```
deployments/kubernetes/agent.yaml              a StatefulSet and a headless Service
deployments/helm/brevis   agent.enabled=true   off by default
```

The chart refuses `agent.enabled` with no `agent.tokenSecret` unless
`agent.insecureNoToken` is set, for the same reason the flag exists.

#### What it gives up, in the same frame

`resources:` means nothing on a host. A step gets whatever the agent's pod has;
a pod asks for its own share, and a guest does not. Steps on one host also share
its CPU and memory with no bound between them. Choose `image:` when a step needs
its own ceiling, `host:` when it needs a runtime somebody else owns.

### Added: a step on a host publishes context the way the docs teach

`$BREVIS_OUTPUT` works over the wire. The agent gives the step a path, turns
what it finds there into the step's context, and the engine hands it to the next
step in `$BREVIS_INPUT` — the same contract a step has inside a pod, so a binary
written for one runs unchanged on the other.

### Fixed: an agent pool refused the only topology it has

The rule that decides whether an advertised address belongs to the same
installation compared DOMAINS, and a pod's DNS name has one label MORE than its
Service's. Every unit test passed; the pool would have rejected every instance's
advertisement and fallen back to the shared address, which is the bug
`--advertise` exists to prevent. It compares containment now, and it was the
cluster that caught it rather than the suite.

### Fixed: the chart deployed an engine two releases behind

`appVersion` said `0.13.0` while `VERSION` said `0.15.2`, so `helm install`
brought up `0.13.0` with every manifest beside it correct. **Nothing read both.**
`image-pins-check.sh` now does, and it fails on exactly that drift.

### Fixed: five manifests could not start

Five of ten set `runAsUser: 65532` and five did not, including the chart's
pre-install migration hook — which means the chart could never install into a
cluster that enforces `runAsNonRoot`. Found by running it in one.

### Upgrading

**Nothing changes for a workflow that does not say `host:`.** There is no
migration, no schema change and no new required configuration: `BREVIS_HOSTS`
unset simply means no host is declared.

⚠️ **Upgrade the engine BEFORE publishing a workflow that names a host.** The
workflow decoder ignores fields it does not know, so an engine older than this
does not refuse `host:` — it drops it, and runs the step's `run:` inside its own
container. A command meant for a machine with the licence on it executes in the
worker, which is the quiet kind of wrong. The strict decoder that would have
refused it is an open gap, listed here rather than left to be discovered.

---

## [0.15.2] — 2026-09-24

### Fixed: a workflow's description took the page over

It shipped as a paragraph above the counts, and a real description is not a
line. `id_profile_economics` writes fifteen — why the cube exists, why it moved
from 05:40 to :40, which upstream refreshes every fifteen minutes, and the
measured cost of both halves. All of it worth having, and all of it pushing the
parameters, the rates and the graph below the fold: the page **about** a
workflow opened on an essay about it.

It now sits behind a question mark beside the name, in the same native
`<dialog>` the run failures already use — ESC closes it, the backdrop dims, and
`#about-<slug>` opens it for whoever receives the link.

The author's line breaks survive. A YAML block scalar is written with breaks
that mean something, the reason in one line and the measurement in the next, and
collapsing them would be the same wall in a smaller box.

A workflow with no description gets no question mark: a control that opens an
empty dialog is a promise the page does not keep.

`layouts.Page` gained `Aside`, which sits beside the title. Separate from
`Actions` because that corner is where a page's verbs go — run, retry — and a
reader scanning for "what can I do here" should not have to tell a verb from a
footnote.

---

## [0.15.1] — 2026-09-24

### Fixed: migration 00012 aborted on any database with history

```
ERROR 00012_metricas_de_carga.sql: failed to run SQL migration:
ERROR: cannot extract elements from a scalar (SQLSTATE 22023)
```

`task_runs.etapas` is `jsonb`, and `jsonb` accepts a **scalar**. The backfill
reads it with `jsonb_array_elements`, which does not: one row holding
`'null'::jsonb` takes the whole migration down. Every database of any age has
such a row — a task_run from before the stages existed, or one whose step died
before writing any.

An empty database passes, which is why it shipped: the backfill reads nothing
and the column's shape never comes up.

**Nothing to clean up first.** Goose runs each migration in a transaction, and
this one rolled back whole: a database that hit the error sits at version 11
with no `load_metrics` table and its data untouched — verified by reproducing
the failure through goose and then applying the fix to the same database.
Upgrading is the entire fix; `UPDATE task_runs SET etapas = '[]'` is not needed
and would erase history.

The guard is at the source rather than in a `WHERE`:

```sql
jsonb_array_elements(
    CASE WHEN jsonb_typeof(t.etapas) = 'array' THEN t.etapas ELSE '[]'::jsonb END)
```

`WHERE jsonb_typeof(t.etapas) = 'array'` also passes today, and it relies on the
planner evaluating that predicate before the set-returning function on the same
row — which nothing obliges it to do.

### Fixed: the test that should have caught it held a copy of the SQL

`load_metrics_test.go` pasted the backfill instead of reading it, so it tested a
copy of the migration rather than the migration. It now reads the statement out
of the embedded file, which means a green test is a statement about what runs.

The case it was missing is there too: a scalar among real rows must not take the
rows beside it down. Both halves are mutation-proved — removing the guard
reproduces the production error inside the test, and a guard that returns `'[]'`
for everything fails on the row it should have kept.

---

## [0.15.0] — 2026-09-16

### Added: `list|<type>` — a param that carries several values

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"
  - name: layers
    type: list|string
    enum: [bronze, silver, gold]   # the enum restricts each ITEM
  - name: days
    type: list|integer
```

The element is `string`, `integer` or `boolean`. Each item is validated on its
own and the error names **which** one failed — `1,x,30` in a `list|integer`
gives `item 2: "x" is not an integer` rather than sending the author to check
three values by hand. An empty item and a repeated one are refused; a repeat
silently doubles whatever the step does per item.

**The value is a comma-separated string everywhere, and that is the design.**
Params travel as a `map[string]string` from the trigger form to the step,
through `runs.params` and `BREVIS_RUN_PARAMS`. Making one of them an array would
turn that map into `map[string]any` — and an SDK older than this unmarshals the
variable into `map[string]string`, so it would fail and **discard every param of
that run**, warning in a log nobody reads while the pipeline runs on defaults.

The comma is also what keeps the command working with no special case:

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

The price is that a comma cannot appear inside an item, which is checked at
publish and at trigger.

### Added: a list with an `enum` is a multi-select in the console

Checkboxes, and no JavaScript: one value per ticked box, joined by the handler.
Each group carries a hidden empty value, and that is what makes "I unticked them
all" different from "I did not touch this" — without it the key would leave the
form and the resolver would fall back to the default, running quietly with the
values the operator had just cleared.

A list with no enum is a text field whose items become removable chips, and with
JavaScript off it stays a field holding `users,orders` — which is what the
server expects either way.

### ⚠️ Upgrade before publishing a workflow that uses a list

An engine older than this refuses `list|string` at publish, as an unknown type.
Nothing that validated before validates differently now: the change is
forward-only, and every workflow in this repository still passes.

### Fixed: the unknown-param-type error was in Portuguese

`param "ufs": tipo "list" desconhecido` is the first thing a consumer reads
about a feature they are trying to use. It now names the types that exist.

---

## [0.14.0] — 2026-09-16

### Added: `persist_context:` — a step declares the persisted keys it touches

```yaml
steps:
  - id: fetch_stations
    run: /usr/local/bin/ana_station
    persist_context: [ana.station_codes]
```

The engine then points that step at the installation's store, through
`BREVIS_PERSIST_URL` and `BREVIS_PERSIST_KEYS`. A step that declares nothing
receives neither, and `sdk/persist` refuses every call naming the flag — so a
missing declaration reads as a missing declaration rather than as an empty key.

**A list and not a boolean.** The alternative is a flat namespace where any step
touches any key, and two workflows that both know a good name for
`station_codes` overwrite each other and both keep running. It is a declaration
and not a sandbox: the pod holds the store's credential either way, so this
catches a typo and puts the dependency between two steps in the workflow file,
where it otherwise lives only in a fetcher's source.

### Added: `BREVIS_PERSIST_URL`

Where persisted context lives — a directory, or a `gs://` or `s3://` prefix. The
installation's, set once; a workflow names a key and never a bucket.

Left empty, a workflow that declares `persist_context` is refused **before its
first step**, naming the missing variable. The alternative is the failure that
looks like success: every read returns absent, the pipeline builds an empty
query, and the run finishes green having fetched nothing.

The 4 KB context between steps is untouched and still needs no store at all.

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
