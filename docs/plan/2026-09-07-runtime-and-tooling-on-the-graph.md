# Showing what a step actually runs, on the graph

**Written on** 2026-09-07 · **Base** engine `v0.6.0`, `sdk/v0.53.0`
**Status** done, 2026-09-07 — steps 1–6 in five commits. §3.3 (the observed
tier) and §10 stay out, as written.

A step on the DAG says its id, its command, its state and how long it took. It
does not say **what it runs in**. Two boxes that look identical can be a Go
binary in a 64Mi distroless image and a dbt project in a 1Gi Python image, and
the screen makes them the same grey rectangle.

This plan adds a runtime chip (Python, Go, Node.js, Java, Rust, PHP, …) and tool
chips (dbt, Spark, …) to each node, sourced from the workflow the API already
interprets.

---

## 1. The hard part is not the chip, it is the truth

The engine already ships a badge of this shape: `SDK v0.53.0`. It is worth
reading why it is trustworthy, because that is the bar this feature has to
clear.

That badge is **observed**. The step announces itself over stdout with
`@brevis:{"type":"sdk","version":"…"}`, the runner reads it, and it lands in
`task_runs.sdk_versao`. Nothing in the YAML can produce it. Its own comment in
`graph.go` says so:

> The badge. It exists because it was OBSERVED: the step announced itself.
> Nothing in the YAML produces it, so it has no way to lie.

A language chip has no such luck. Nothing announces "I am Python". So the
feature has to choose between three sources, and **they are not equally true**:

| source | how | can it be wrong? |
|---|---|---|
| **observed** | the process says so, at run time | no — but almost nothing says it today |
| **declared** | the author writes `runtime: python` in the YAML | yes: it can go stale against the image |
| **inferred** | the engine reads `run:` and `image:` | yes: it is a guess |

The temptation is to ship inference alone, because it works on every existing
workflow with zero YAML changes, and to paint it like the SDK badge. That would
be the exact failure this repository keeps naming: a badge that can lie is worse
than no badge, because a badge is what somebody looks at to rule a hypothesis
out.

**So all three ship, the precedence is observed > declared > inferred, and the
UI shows which one it got.** An inferred chip is drawn differently from a
declared one. That is not decoration: it is the difference between "this step
runs Python" and "this step's command starts with `python`".

---

## 2. The model

One **runtime** and zero or more **tools**, per step.

```
runtime: the language or execution environment the process is
tools:   the frameworks or CLIs it drives
```

They are separate because they answer different questions and because one step
routinely has both. `dbt build` is Python underneath and nobody cares; what the
operator wants on the card is `dbt`. `spark-submit --py-files …` is Spark
running Python, and both matter — Spark explains the memory, Python explains the
stack trace.

Collapsing them into one list would force a ranking that has no right answer.
Keeping runtime singular is what makes the card readable: one strong chip, then
the tools.

### The vocabulary is closed, and that is deliberate

A fixed set of ids, each with a display name and a colour:

```
python go node java rust php ruby dotnet shell sql
dbt spark airbyte soda greatexpectations sqlmesh meltano pandas polars duckdb
```

Unknown means **no chip**, never a chip reading "unknown" or "shell". A card
that says nothing is honest; a card that says the wrong thing costs somebody an
hour. Growing the list is a one-line table entry plus its test, which is the
point of it being a table.

`shell` is in the runtime list but is the **last** resort and only when the
command is unambiguously shell work (`cp`, `aws s3 sync`, a `.sh` script). A
step whose command is `{{ .cmd }}` gets nothing.

---

## 3. Detection, in the order it should be tried

### 3.1 Declared — the new YAML fields

```yaml
steps:
  - id: transform
    image: ghcr.io/acme/dbt:1.7
    run: dbt build --select gold
    runtime: python          # optional
    tools: [dbt]             # optional
```

Both optional and both validated at **publish** time against the closed
vocabulary, the way `params` and `resources` already are — an unknown id is an
error naming what exists, refused by `brevis publish`, not discovered on the
screen three days later.

`StepSpec` gains `Runtime string` and `Tools []string`; `workflow.Node` gains
the same two.

**A note on the persisted format**, because this is where the last plan lost a
day: a `Workflow` is stored whole as JSON in `workflows.definicao` and
`runs.definicao` **with no tags**, so the Go field name is the key. Adding
fields is additive and safe in both directions — an old engine ignores keys it
does not know, and a new engine reading an old document gets the zero value,
which means "not declared" and falls through to inference. Nothing to migrate.
`Param` carries explicit tags for the fields that were renamed; these are new,
so they need none. See `TestTheParamKeysAreTheOnDiskFormat`.

### 3.2 Inferred — reading `run:` and `image:`

A pure function, in a package of its own:

```go
// internal/domain/runtime
func Detect(run, image, action string) Detection
type Detection struct {
    Runtime string   // "" when unknown
    Tools   []string // sorted, may be empty
    Source  string   // "inferred"
}
```

**Pure and exported on purpose.** This repository has already paid for the
alternative once — `load.CreationPlan`'s doc says it plainly: a decision made
inside a method that holds a client is never seen by a test. `Detect` takes
three strings and returns a value, so every rule is exercised without a cluster,
an image, or a Postgres.

#### The command is not a single word

`run:` reaches the container as `sh -c "<the whole string>"` unless
`shell: false`, and authors write shell. The detector has to survive at least:

```
python fetch.py                                   → python
python3 -m pip install -r req.txt && python x.py   → python
FOO=bar python x.py                                → python   (env prefix)
cd /src && dbt build                               → dbt      (cd prefix)
uv run python -m app                               → python
/opt/brevis/bin/fetch-weather                      → (nothing) — a bare binary
dbt build --select gold | tee log.txt              → dbt
spark-submit --py-files a.zip job.py               → spark + python
python {{ .script }}                               → python
{{ .cmd }}                                         → (nothing)
```

The approach: split on shell separators (`&&`, `||`, `;`, `|`, newline), and for
each fragment strip leading `VAR=value` assignments and known prefix commands
(`cd X &&` is already handled by the split, `env`, `exec`, `time`, `nohup`,
`uv run`, `poetry run`, `pipenv run`, `npx`, `bunx`). Then match the head token
against the table, and scan the whole fragment for tool markers.

**Every fragment contributes.** `python -m pip install && dbt build` is a dbt
step whose runtime is Python, and taking only the first fragment would call it
Python-only while taking only the last would lose the runtime.

This is where the feature earns or loses its reputation, so it gets a **table
of cases as data**, one row per shape above, and the table is the test.

#### The image is a second, independent signal

`image: ghcr.io/acme/dbt-runner:1.7` says dbt more reliably than any command
does. The rule is a substring match on the image's **repository path only** —
never the tag, never the registry host, because `ghcr.io/python-shop/anything`
is not Python.

When the command and the image disagree, **the command wins and the image adds
tools**. The command is what actually executes; the image is what it executes
inside. An image named `python:3.12` running `dbt build` is a dbt step.

#### `action:` is exact

A step with `action:` runs a registered Go task in the engine's own process.
That is `go`, with certainty, and it needs no heuristic — `Detect` returns it
directly.

### 3.3 Observed — later, and the door is already open

The `@brevis:` protocol has a `type` field and this engine already accepts two
formats of it with a documented bridge (see `stages.go`, and the `v0.48.0`
entry). A future `{"type":"runtime","name":"python","version":"3.12"}` would
land the observed tier with the version attached, exactly as the SDK badge does.

**Not in this plan.** It changes a published wire protocol and needs the SDK to
emit it, which is a separate release on a separate module. The plan's job here
is to make sure the model does not have to change when it arrives: `Source` is
already a field, and `observed` is already the top of the precedence list.

---

## 4. Where it is computed, and why not in the database

At **request time**, in `internal/api/graph.go`, from the definition it already
loads.

The alternative is a column on `workflows` filled at publish. It is worse for
one reason that outweighs the rest: inference rules will be wrong at first, and
a stored value means a wrong guess is frozen into every workflow published
before the fix, needing a backfill to correct. Computed on read, fixing the rule
fixes history.

The cost is real and small: the graph endpoint is polled every 2s while a run is
live, and this is string work over a handful of nodes. If it ever shows up in a
profile, the answer is a memo keyed by the definition's hash — not a column.

---

## 5. What the payload carries

```go
data["runtime"] = "python"
data["runtime_source"] = "inferred"      // observed | declared | inferred
data["tools"] = []string{"dbt"}
```

Omitted entirely when there is nothing to say, so an unknown step's payload is
byte-identical to today's.

### The Portuguese keys, and whether this is the moment

`dag.js` carries a note saying the payload's Portuguese keys — `nome`, `estado`,
`numeros`, `rotulo`, `acao`, `erro`, `tentativa`, `duracao_ms` — stay until
there is a reason to touch the payload anyway, because renaming them costs a
broken render for anyone holding the page across a deploy.

This is arguably that moment. It is also the moment to **not** do it: this
change already touches the payload, the graph builder and the island, and
folding a rename in makes one diff that cannot be reviewed as either.

**Decision: no.** The new keys go in English, the old ones stay, and the file's
note gets one line saying the rename is now the only thing left holding those
words. A mixed payload is ugly for one release and honest about why.

---

## 6. The UI

A chip row under the command line, above the stages.

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.53.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

- **Runtime chip**: filled, with the language's colour.
- **Tool chips**: outlined, neutral.
- **Inferred** chips carry a dotted border and a `title` reading
  *"inferred from the command"*. Declared and observed are solid. The
  difference has to be visible without hovering, or it is not a distinction.

### Icons

None, and this is a constraint rather than a preference. `web/` is hand-written
vanilla JS with no bundler — §15 of the original plan forbids Node in the build
— and the assets are embedded and served by the binary, so a CDN is out. That
leaves vendoring a sprite, which is weight for decoration.

**Coloured text chips.** They read at a glance, they cost nothing, and they
inherit the installation's theme through `internal/branding` like everything
else on the card. The palette needs new tokens (`--color-lang-python`, …) with
defaults, and they must survive a custom `brand.yaml`, which today has no
opinion about them.

### The detail panel

The card is cramped; the panel is not. It shows the same information with its
provenance spelled out: *"Python — inferred from `python fetch.py`"* or
*"dbt — declared in the workflow"*. That is where somebody goes when the chip
surprises them, and it has to answer why without them opening the YAML.

---

## 7. What this must not break

- **A step with no detectable runtime renders exactly as it does today.** No
  empty row, no reserved space, no "unknown" chip.
- **The graph endpoint's shape stays backward compatible.** New keys are
  additive; a browser holding an old `dag.js` ignores them and draws the card it
  drew before.
- **`brevis publish` keeps accepting every workflow it accepts today.** The new
  fields are optional; only a *wrong* value is refused.
- **No new dependency.** The detector is `strings`; the chips are inline styles.
  The engine's package ceiling is 330 and it sits at 297.

---

## 7.5 What changed while building it

Three corrections the work forced, kept here because the plan was wrong about
them and the reasons are the useful part.

**The package is `runtimes`, not `runtime`.** `cmd/brevis` imports the standard
library's `runtime`, and the collision would force an import alias on every file
that wanted both.

**`action:` contributes nothing.** §3.2 said a step with `action:` is `go` "with
certainty". It is not: two of this repository's own examples use
`action: kubernetes.run` and `action: docker.run`, dispatch actions whose
payload is an arbitrary image, and the first implementation labelled both Go.
Telling a real in-process task from a dispatch one needs the executor's
registry, which a pure function does not have — so blank is the honest answer.
The corpus test in step 5 is what caught it, on its first run, which is the
argument for the corpus test.

**`shell` had to become the weakest runtime.** First match wins across fragments
read `cp in.csv /tmp/ && python x.py` as a Shell step and hid the Python — the
language whose stack trace the operator is about to read. Anything concrete now
beats `shell`.

**Precedence lives in the domain package, not in `graph.go`.** §5 put it in the
handler. `runtimes.Resolve` is next to `Detect` instead, so the order is a
property of the package with a test, rather than a line in an HTTP handler
nobody exercises.

---

## 8. Order of work

Each step lands green, with the gates passing.

1. **`internal/domain/runtime`** — the vocabulary, `Detect`, and the case table.
   No caller yet. This is the whole risk of the feature and it is testable in
   isolation, so it goes first and alone.
2. **The declared fields** — `StepSpec`, `Node`, publish-time validation with an
   error naming the valid ids, and a test that an old stored definition round
   trips.
3. **`graph.go`** — precedence, the payload keys, omitted when empty.
4. **`dag.js`** — the chips and the panel, plus the branding tokens.
5. **`examples/`** — the workflow YAMLs gain `runtime:`/`tools:` where they
   apply, so the feature has a worked example that runs.
6. **`docs/`** — `PARAMS.md`'s neighbour for the new fields, and a line in
   `COMMANDS.md` where `publish` documents what it validates.

## 9. How it is proven

- **A case table for `Detect`**, one row per shape in §3.2, including the ones
  that must return nothing. A rule with no row is a rule nobody has checked.
- **A corpus test** over every YAML in `examples/` and `deployments/`: parse,
  detect, and assert the result against a golden file. It is what catches a rule
  that becomes over-eager, because the corpus contains steps that must stay
  blank — `/opt/brevis/bin/fetch-weather` is a bare binary and has to detect as
  nothing.
- **A precedence test**: declared beats inferred, and a declared value that
  contradicts the command is honoured, because the author is allowed to know
  better than the parser.
- **Proof the tests bite**, per `CONTRIBUTING.md`: delete a table row and the
  case fails; invert the precedence and the precedence test fails.

## 10. What is deliberately not here

- **The observed tier.** §3.3 — it needs an SDK release and a wire-format
  change, and the model is already shaped for it.
- **Versions on the chip.** `Python 3.12` is more useful than `Python`, and
  neither the command nor the image reliably carries it — the tag `:3.12-slim`
  does, sometimes, and `:latest` does not. Guessing a version is worse than
  omitting one. It arrives with the observed tier or not at all.
- **Detecting from the repository's files.** Reading a `requirements.txt` or a
  `go.mod` next to the workflow would be more accurate and would mean the engine
  reading the client's source tree, which it does not do and should not start.

---

## 11. One crumb found while reading `dag.js`

`web/assets/dag.js` still holds a comment cut in half by an earlier pass:

```js
// Without this the click would also select the step, and
// collapsing
// abriria o painel de detalhes junto.
```

Step 4 touches that file. It gets finished there rather than opening a thread of
its own.
