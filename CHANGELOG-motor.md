# Changelog — the engine

The engine's versions, published as a Docker image (`daniel3843/brevis`). The SDK
has its own, in [`CHANGELOG.md`](CHANGELOG.md): they are two artifacts with
different audiences — one is a Go module somebody imports, the other an image
somebody operates — and that is why there are two lists.

The engine's tag is `vX.Y.Z`, with no prefix; the SDK's carries `sdk/`.

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
