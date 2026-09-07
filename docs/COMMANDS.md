# Commands

The command-line reference. The outputs below were captured from the binary
built at this commit, not written by hand.

## Two binaries, similar names

|  | `brevis` | `brevis-sdk` |
|---|---|---|
| **what it is** | the engine: it orchestrates, schedules, executes and serves the UI | the SDK's CLI: it extracts from HTTP and loads into BigQuery |
| **source** | [`cmd/brevis/`](../cmd/brevis/) | [`cmd/brevis-sdk/`](../cmd/brevis-sdk/) |
| **module** | the repository's | its own (a separate `go.mod`, with the SDK pinned by version) |
| **install** | `make build` → `bin/brevis` | `go install github.com/AreteAcademy/brevis/cmd/brevis-sdk@latest` |
| **needs Postgres** | for most subcommands | never |

## Installing

```bash
make build                    # engine → bin/brevis, with version and commit stamped in
docker run daniel3843/brevis:latest version
go install github.com/AreteAcademy/brevis/cmd/brevis-sdk@latest
```

The engine is released as tags on the root module (`v0.6.0` at the time of
writing) and as images. `go install
github.com/AreteAcademy/brevis/cmd/brevis@latest` resolves to the latest tag,
but the binary it produces reports `brevis dev`, with no commit and no date:
the `-ldflags` only come in through `make build` and `docker build`. For a
traceable version, use the **image** or a local `make build`.

---

# `brevis` — the engine

```
Brevis — a data transformation and orchestration engine

Available Commands:
  backfill    Materialize a workflow's past slots
  brand       Validate a brand file (needs no database)
  hash        Generate the BREVIS_AUTH_SENHA_HASH hash (reads the password from the terminal)
  migrate     Apply the schema migrations
  publish     Publish workflows and their schedules to the database
  run         Run a workflow locally
  scheduler   Materialize schedules into runs and execute them
  serve       Start the HTTP API
  validate    Validate workflow files (needs no database)
  version     Print the binary's version
```

| command | database | credential outside `local` | role |
|---|---|---|---|
| [`serve`](#brevis-serve) | **yes** | **required** | API + UI |
| [`scheduler`](#brevis-scheduler) | **yes** | **required** | both loops: it creates and it executes |
| [`migrate`](#brevis-migrate) | **yes** | **required** | schema |
| [`publish`](#brevis-publish) | **yes** | **required** | writes the workflow and the schedule |
| [`backfill`](#brevis-backfill) | **yes** | **required** | reprocesses a range |
| [`run`](#brevis-run) | no | — | runs now, on the instance itself |
| [`validate`](#brevis-validate) | no | — | validates workflow YAML |
| [`brand`](#brevis-brand) | no | — | validates brand YAML |
| [`hash`](#brevis-hash) | no | — | generates the password hash |
| [`version`](#brevis-version) | no | — | version, commit, build |

The database column comes from a single fact: the first five call
`config.Load()`, which **fails at boot** without `BREVIS_DATABASE_URL`. The
other five do not call it — and that is why `validate` works in CI, where there
is no Postgres.

The same five inherit the credential rule: with `BREVIS_ENV` set to anything
other than `local`, starting without `BREVIS_AUTH_USUARIO` +
`BREVIS_AUTH_SENHA_HASH` + `BREVIS_AUTH_SEGREDO` is a **boot error**, not a
warning. The UI fires `dbt build` against the warehouse; open on the internet,
it is a remote control for the warehouse.

---

## `brevis serve`

Starts the HTTP API and the interface. No flags — everything comes from the
environment.

```bash
brevis serve
```

It shuts down on `SIGINT`/`SIGTERM` with a graceful shutdown of
`BREVIS_SHUTDOWN_TIMEOUT_SECONDS` (15 s by default), so a deploy does not cut
requests in flight.

This process **does not materialize schedules**. The screen's manual trigger
calls the same scheduler, without the loop — the rule "the scheduler creates the
runs" keeps a single owner. For the schedules to run, a `brevis scheduler` has
to be running alongside.

It is the `api` image's `CMD` (distroless, no shell).

---

## `brevis scheduler`

The system's two loops, in the same process and independent: the **scheduler**
materializes schedules into runs, the **dispatcher** takes them off the queue and
executes them. One can go down without interrupting the other.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | type | default | |
|---|---|---|---|
| `--interval` | duration | `10s` | interval between the scheduler's cycles |
| `--concurrency` | int | `5` | simultaneous **runs** |
| `--max-pods` | int | `5` | simultaneous **steps** in total |

`--concurrency` and `--max-pods` count different things, and that is deliberate:
five runs with three parallel steps each would mean fifteen pods if the run
limit were the only one.

Where each step runs depends on `BREVIS_PODS` and on whether there is a cluster:

| `BREVIS_PODS` | with a cluster | without one |
|---|---|---|
| `auto` (default) | a step with `image:` becomes a pod | everything runs as a local process, with a warning in the log |
| `on` | a step with `image:` becomes a pod | **boot error** |
| `off` | everything runs as a local process | everything runs as a local process |

`on` exists for the deployment that must not silently become local execution.

Without `BREVIS_SLACK_WEBHOOK` the process warns at boot that failures will not
be announced — an installation that fails in silence is discovered by the
customer, not by the team.

It is the `worker` image's `CMD` (alpine with a shell, because the `run:` steps
need one).

---

## `brevis migrate`

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # undoes the last one
brevis migrate status  # shows the state
```

It takes exactly one argument, one of `up`, `down` and `status`. The migrations
are embedded in the binary (`migrations/`) and are applied by a subcommand of
their own — **`serve` never alters the schema**.

Without the database variable, it fails immediately:

```
$ brevis migrate status
error: BREVIS_DATABASE_URL is required
```

---

## `brevis validate`

Validates one or more workflow files. It takes a file **or a directory** — in a
directory it matches `*.y*ml` and sorts, so two runs produce the same log.

```bash
$ brevis validate examples/
  ok    daily_analytics              dag  5 steps, 5 dependencies  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencies  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

It does not touch the database and starts no server, so it runs in the editor and
in CI. It exits non-zero and counts the failures:

```
$ brevis validate broken.yaml
  ERROR broken.yaml: workflow "x" has no steps at all
error: 1 of 1 file(s) had errors
```

---

## `brevis run`

Runs a workflow **now, on the instance itself**: no queue, no database, no
scheduler.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| flag | type | default | |
|---|---|---|---|
| `--param` | repeatable | — | `key=value` for a parameter declared in the workflow |
| `--workdir` | string | the file's directory | the steps' working directory |
| `--retries` | int | `1` | attempts per step (`1` = no retry) |
| `--timeout` | duration | `0` | timeout per step (`0` = no limit) |

`--param` with no `=` is an **error**, not a warning: `--param load_full` would
run with the default and the operator would believe the value had been applied.

Three limits worth knowing before using it in production:

- **It only operates with `BREVIS_ENV=local`** (empty counts as local). Outside
  that, the process executor refuses to be built.
- **A step with `image:` runs on the instance itself**, not in a pod — and says
  so. `run` assembles no Kubernetes executor; the `scheduler` is what does.
- **There is no Go task registry.** An `action:` for an unregistered task fails
  naming the ones that exist, because tasks are registered by whoever compiles
  the binary and the generic CLI knows none.

The output is prefixed by the step, which is what keeps it readable when several
run in parallel at the same level:

```
workflow hello (dag, 4 steps) in examples
  ▶ preparar
    preparar | preparando
  ✓ preparar
  ▶ validar
  ▶ extrair
```

---

## `brevis publish`

Writes the workflows and the schedules to the database. It takes a file or a
directory, like `validate`.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| flag | type | default | |
|---|---|---|---|
| `--project` | string | `default` | the project's slug |
| `--prune` | bool | `false` | removes from the project the workflows absent from the published list |

It refuses the whole publish on the first invalid workflow: a duplicated step
id, a dependency that does not exist, a cycle, a malformed resource quantity, a
param whose default fails its own type, an environment variable name a shell
would not accept, and a `runtime:` or `tools:` outside the vocabulary — that
last one naming what IS valid, see [`RUNTIME.md`](RUNTIME.md).

Refusing here is the point: every one of those is otherwise found on a screen,
days later, by somebody who did not write the file.

```
$ brevis publish examples/
  published  daily_analytics          (manual)
  published  daily-report             cron 0 2 * * *
  published  hello                    (manual)
```

**`--prune` is optional and not the default** for a practical reason: `publish
one-file.yaml` must not delete the project's other 48 just because they were not
named on the command line. With `--prune`, the removed ones keep their history.

The project is created if it does not exist (`ON CONFLICT DO UPDATE`), which
keeps the FK honest while there is no project management.

---

## `brevis backfill`

Materializes the past slots of an already-published workflow. It **queues, it
does not execute** — the `scheduler` is what executes.

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| flag | type | | |
|---|---|---|---|
| `--from` | string | **required** | start date, `YYYY-MM-DD` |
| `--to` | string | **required** | end date, `YYYY-MM-DD` |
| `--param` | repeatable | — | applies to **every** slot in the range |

`--to` includes the whole day: internally the end becomes `23:59:59` of that
date. A date in another format is an error with the hint built in (`use
YYYY-MM-DD`).

```
  31 backfill run(s) queued for daily (2026-01-01 to 2026-01-31)
  run `brevis scheduler` to execute them
```

The central use case is exactly "reprocess the whole of January with
`load_full=true`".

---

## `brevis brand`

Validates a visual-identity file without starting the server.

```bash
$ brevis brand brand.example.yaml
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    #aa8450
        Powered by Brevis
```

`brevis marca` still works as an alias: that was the command's name in a
released version, and a script calling it must not start printing "unknown
command".

It exists for the same reason `validate` does: a wrong hex value in `brand.yaml`
would only surface when the container started, and the message would arrive
through the pod's log — far from whoever edited the file. Here the error comes
back in the pull request.

An unrecognized key is an error too. Before v0.7 it was ignored, so a typo left
the installation on the default identity with nothing said; the field names also
became English in that version, and a file still using `titulo:` fails naming
it.

One behavioural difference from boot: **a missing file is an error**. In `serve`,
absence means "use the default identity"; whoever asked to validate a path
expects to be told it does not exist.

---

## `brevis hash`

Generates the `BREVIS_AUTH_SENHA_HASH` hash.

```bash
$ brevis hash
password:
BREVIS_AUTH_SENHA_HASH:
pbkdf2-sha256$...

Still missing: BREVIS_AUTH_USUARIO and a BREVIS_AUTH_SEGREDO of 32+ bytes
(openssl rand -base64 48).
```

The password is read from the terminal **without echo**, never from an argument:
an argument shows up in any process's `ps` on the machine and stays in the
shell's history. With the input redirected (a provisioning script), it reads from
standard input.

It refuses a password under 12 characters — this is the only way into the panel.

The hash goes to **stdout** on its own and the labels to **stderr**, so `brevis
hash > hash.txt` writes only what matters.

---

## `brevis version`

```bash
$ brevis version
brevis 0.6.0
  commit  bb832ff
  build   2026-09-06T12:00:00Z
  go      go1.27.0 darwin/arm64
```

The version, the commit and the date are stamped at build time by `-ldflags`.
Built straight with `go build`, it prints `brevis dev` with no commit and no
date — and telling that apart from a release artifact matters when somebody
reports odd behaviour. `make image` appends `-dirty` to the commit when there is
an uncommitted change.

---

# `brevis-sdk` — the SDK's CLI

Extracts from HTTP and loads into BigQuery without writing Go. A module of its
own, with the SDK pinned by version.

```bash
go install github.com/AreteAcademy/brevis/cmd/brevis-sdk@latest
```

| command | |
|---|---|
| [`extract`](#brevis-sdk-extract) | extracts from a URL and prints |
| [`load`](#brevis-sdk-load) | loads NDJSON from standard input into BigQuery |
| [`run`](#brevis-sdk-run) | extracts and loads in one command |
| `version` | version and commit |

## `brevis-sdk extract`

```bash
brevis-sdk extract https://api.example.com/data.csv
brevis-sdk extract https://api.example.com/data.json --format json --output json
brevis-sdk extract https://api.example.com/data --retries 5 --timeout 60s
```

| flag | short | type | default | |
|---|---|---|---|---|
| `--format` | `-f` | string | empty | `csv`, `json`, `ndjson`, `xml`; empty tries to auto-detect |
| `--timeout` | `-t` | duration | `30s` | timeout per attempt |
| `--total-timeout` | | duration | `5m` | timeout across all attempts |
| `--retries` | `-r` | int | `3` | maximum attempts |
| `--output` | `-o` | string | `table` | `table` or `json` |

With `--output json`, every row comes out as a JSON object — the format that
feeds a `brevis-sdk load` through a pipe. An unrecognized format falls back to
CSV in silence.

## `brevis-sdk load`

Reads NDJSON from standard input and loads it into BigQuery.

```bash
brevis-sdk extract https://api.example.com/data.csv --output json \
  | brevis-sdk load --project my-project --dataset landing --table raw_data
```

| flag | short | default | |
|---|---|---|---|
| `--project` | `-p` | — | **required** |
| `--dataset` | `-d` | `landing` | |
| `--table` | `-t` | `raw_data` | |
| `--metadata` | `-m` | `false` | adds `ingestion_id` and `ingestion_loaded_at` |

Empty input is refused rather than loaded: a pipe whose upstream produced
nothing used to be indistinguishable from one that worked. A line that does not
parse is named.

## `brevis-sdk run`

Extracts from a URL and loads into BigQuery in one command.

```bash
brevis-sdk run https://api.example.com/data.csv --project my-project
brevis-sdk run https://api.example.com/data.csv --project my-project --dry-run
```

| flag | short | default | |
|---|---|---|---|
| `--project` | `-p` | — | **required** |
| `--dataset` | `-d` | `landing` | |
| `--table` | `-t` | `raw_data` | |
| `--metadata` | `-m` | `false` | |
| `--dry-run` | | `false` | extracts and stops before loading |

**It only reads CSV.** The format is fixed at `sdk.FormatCSV` in the code, and
there is no `--format` flag here. A JSON URL is read as CSV and the result is
useless.

For anything beyond that, the Go SDK is the path — a whole fetcher fits in
twenty lines, with flags, retry, pagination, provenance and the exit code all
coming from `sdk.Run`. See
[`examples/08-minimal-fetcher`](../examples/08-minimal-fetcher/).

---

# Environment variables

Read once, at boot, by `config.Load()` — nothing consults the environment
afterwards.

## Required

| variable | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Absent = **boot error** in the five subcommands that use the database |

## Process

| variable | default | |
|---|---|---|
| `BREVIS_ENV` | `local` | `local` uses text logging and opens the UI without a password; anything else requires a credential and logs JSON |
| `BREVIS_HTTP_ADDR` | `:8080` | listen address |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | an integer; a non-numeric value is a boot error |
| `BREVIS_BRAND_FILE` | `brand.yaml` | visual identity; absent = the default |
| `BREVIS_UI_URL` | — | the base of the run's link in an alert |
| `BREVIS_SLACK_WEBHOOK` | — | where the definitive-failure alert goes. Empty = nobody is told |

## Authentication

| variable | |
|---|---|
| `BREVIS_AUTH_USUARIO` | the panel's user |
| `BREVIS_AUTH_SENHA_HASH` | a `pbkdf2-sha256$...` hash, generated by `brevis hash` |
| `BREVIS_AUTH_SEGREDO` | 32+ bytes to sign the session (`openssl rand -base64 48`) |

All three come together or none does. **Half-configured is a boot error**:
whoever filled in the user believes they closed the door, and a warning in the
log does not undo that belief.
