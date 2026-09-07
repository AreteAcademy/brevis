# A Node.js context library

**Written on** 2026-09-08 · **Base** engine `v0.7.0`, `brevis` (py) `0.1.1`
**Status** proposed — not started · **TASK.md #4**

The third language on a contract that already has two. Most of this plan is
short, and that is the point: the design work was done for
[`2026-09-07-python-context-sdk.md`](2026-09-07-python-context-sdk.md), the
contract shipped, and a three-language test already proves it is not
Go-specific.

What is left is the parts where Node differs.

---

## 1. "How does it talk to the API?" — it does not

The same answer as Python, and it is worth repeating because it is the question
that gets asked every time:

```
BREVIS_INPUT    {"<step id>": {…}}   ← the engine sets it when it starts the step
BREVIS_OUTPUT   a path to write once  ← /dev/termination-log in a pod
```

The step's process opens no socket. The kubelet copies that file into the pod's
status; the scheduler was already fetching that status for the exit code; it
writes `task_runs.saida`; the API pod reads it from Postgres for the screen.
`docs/CONTEXT.md` traces all six steps.

So this library is **a JSON parse and a file write** — which is why it can have
zero dependencies and why its tests need no cluster, no database and no engine.

Everything the request asks for is already true: the context is in the database,
the run id is the session id and is already injected as `BREVIS_RUN_ID`, and a
resumed run reads it back.

---

## 2. The API, and the two corrections it needs

The requested shape:

```js
import { BrevisContext } from '@brevis';
const bucket = BrevisContext.get('bucket');
BrevisContext.set({ bucket: 'gcs://' });
```

What ships, and why each difference exists:

```js
import { context } from '@brevis/context';

const bucket = context.get('extract.bucket');
context.set({ bucket: 'gcs://…' });
context.set({ rows: 48213 });          // merges — both keys survive
```

**A qualified key.** `get('bucket')` with no step is refused, naming the steps
that published it. Steps are isolated — `extract` and `transform` can both
publish `bucket` and neither loses it — and a bare key throws that away the
moment two of them do. Searching and picking one is precedence by accident: it
works until it silently does not.

This is the same correction the Python library carries, and the two must not
disagree: a team with a Python step and a Node step next to it should not find
that one of them guesses.

**A module, not a class.** `BrevisContext.get(…)` as a static suggests there
could be two instances. There cannot: a process is inside exactly one step of one
run.

**`set` merges.** Two calls leave both keys; the same key twice keeps the last.
Replacing wholesale would let a helper publishing one key silently erase what its
caller published — a bug that only appears in the step after.

---

## 3. Where Node actually differs from Python

### Writing at exit

Python defers to `atexit`; Go writes on every `set` because it has no atexit.
Node has `process.on('exit')`, and **it only accepts synchronous work** — which
is exactly what this is:

```js
process.on('exit', () => { fs.writeFileSync(path, payload); });
```

So Node matches Python: accumulate, write once, and a step that dies hard
publishes nothing — which is correct, because a crashed step's output described
work that did not finish.

**But `process.on('exit')` does not fire on every death**, and the ones it misses
have to be named: `SIGKILL` (nothing fires), an uncaught exception (fires only
after `uncaughtException` handlers), and `process.exit()` inside a promise that
never settles. The library registers for `exit`, and documents that a `SIGTERM`d
step publishes nothing unless the program handles the signal.

### ESM and CommonJS

A data team's Node is not one Node. The package ships **both**, through
`exports`:

```json
"exports": { ".": { "import": "./dist/index.mjs", "require": "./dist/index.cjs", "types": "./dist/index.d.ts" } }
```

Shipping only ESM makes it unusable from a `require`-based script, which is a
large share of the scripts this exists for.

### TypeScript types

Hand-written `.d.ts`, not generated, and **not a build step**. The source is
plain JavaScript for the same reason `web/assets` is: this repository forbids a
Node toolchain in its own build, and a library that needs a bundler to be read is
a library nobody reads. The types are ~30 lines and they are part of the API.

### Zero dependencies

`fs`, `path`, `process`. Nothing else, ever, and a CI gate asserts it — the same
gate the Python library has, which fails when an import appears outside an
allowlist.

A `node_modules` this package contributes to is a `node_modules` somebody has to
audit.

---

## 4. Packaging

| | |
|---|---|
| npm name | `@brevis/context` — a scope needs an npm organisation, which is the one manual step |
| import | `import { context } from '@brevis/context'` |
| Node | **18+**, the oldest release still maintained |
| dependencies | none |
| layout | `lib/node-context/`, alongside `lib/python-context/`, per `lib/README.md` |
| tag prefix | `node/v*`, a third cadence beside `v*` and `sdk/v*` |
| publishing | npm trusted publishing (OIDC), the same shape as PyPI's — no token in a secret |

**The unscoped name `brevis` is also worth checking** before the scope is
created. The Python library ended up as `brevis` rather than `brevis-context`
because the name was free, and matching install name to import name is the least
surprising thing.

---

## 5. Order of work

| | |
|---|---|
| 1 | `lib/node-context`: the module, ESM + CJS, the `.d.ts` |
| 2 | its tests — env var in, temp file out, no engine |
| 3 | the four-language end-to-end test: bash, Go, Python **and Node** in one run |
| 4 | `.github/workflows/publish-node.yml`, with the same four gates the Python one has |
| 5 | `docs/CONTEXT.md` gains a Node column; `lib/README.md` gains a row |

Step 3 is the acceptance criterion. The three-language test already exists and
adding a fourth step to it is small — and it is what proves Node reads what
Python wrote, which no amount of unit testing on either side can.

## 6. How it is proven

Everything the Python library asserts, plus:

- **Node reads what Python published, and Python reads what Node published**, in
  one run, through real processes.
- **The wire format is one flat object.** Pinned on the Node side too, because
  four implementations now read it and a wrapper added in one is invisible until
  another fails to find a key.
- **`require()` and `import` both work**, from a packed tarball rather than from
  the source tree — the way a consumer gets it.
- **Two `set` calls merge**, which is the question that gets asked about every
  one of these libraries.
- **The dependency gate fails** when an import outside the allowlist appears.

## 7. What it is not

Not a port of the Go SDK. No drivers, no pagination, no checkpoints, no
ingestion ids. Node has its own ecosystem for those, and Brevis's job is to run
what a team writes and carry a few kilobytes between their steps.
