# Open threads

**Written** 2026-09-06 · **State** every item below was verified against the
tree at `2b708da`, not recalled from memory. Line numbers are from that commit.

This is a closing inventory, not a wish list. It exists so the next window of
work can start on a clean surface: each item says what is open, where it is,
why it matters, and what closing it costs. Two threads raised earlier are
already closed and are recorded here so nobody reopens them.

---

## Closed since they were raised

| thread | how it closed |
|---|---|
| **The engine CLI had no subcommands.** `cmd/bravis` registered only `extract`, `load`, `run` and `version` — the SDK's CLI — while the README, the compose file and the Dockerfile all invoked `serve`, `scheduler` and `migrate`. | Split into two commands. `cmd/brevis` now registers all ten engine subcommands; the SDK's CLI moved to `cmd/brevis-sdk` with its own module. |
| **The root module had no tag.** Every tag was `sdk/v*`, so `go install .../cmd/brevis@latest` resolved to a branch pseudo-version and produced a binary reporting `brevis dev`. | The root module is tagged. `v0.4.0`, `v0.5.0`, `v0.6.0` exist; `VERSION` reads `0.6.0`. |

---

## 1. The public surface mixes two languages

`CONTRIBUTING.md` now states the rule: the repository is English, and only the
website's user-facing documentation is translated. The rule reaches further than
the prose — it reaches everything a user types.

| what | where | today |
|---|---|---|
| a subcommand name | `cmd/brevis/main.go` | `brevis marca` |
| four environment variables | `internal/config/config.go` | `BREVIS_AUTH_USUARIO`, `BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO`, `BREVIS_POD_MANTER_EM_FALHA` |
| the branding file's keys | `brand.example.yaml` | `titulo`, `subtitulo`, `frase`, `tema` |
| every command's one-line help | `cmd/brevis/main.go` | "Sobe a API HTTP", "Materializa slots passados de um workflow" |
| error messages | `internal/**` | `BREVIS_DATABASE_URL e obrigatoria`, `titulo nao pode ser vazio` |

**Why it matters.** A contributor in Berlin reads `BREVIS_POD_MANTER_EM_FALHA`
and cannot guess what it does. Worse, the mix is arbitrary: the same struct
holds `BREVIS_POD_NAMESPACE` and `BREVIS_POD_MANTER_EM_FALHA`, so there is no
rule a reader can infer — only history.

**What closing it costs.** This is the only item on this page that **breaks
running installations**. A renamed environment variable silently stops being
read; a renamed `brand.yaml` key silently falls back to the default. Both fail
the project's own rule against dropping data in silence.

The shape that respects it:

- accept both names for one minor version, with the old one logging a warning
  that names its replacement;
- make the *absence* of a value visible — if `BREVIS_POD_MANTER_EM_FALHA` is set
  and `BREVIS_POD_KEEP_ON_FAILURE` is not, say so at boot rather than ignoring
  it;
- drop the old names in the release after, and say so in the changelog.

For `brevis marca` the same applies: register `brand` as the name and keep
`marca` as a hidden alias for one version.

Error messages and `Short:` strings have no compatibility cost. They can change
in one pass, and they are the cheapest half of this item.

---

## 2. What the website shipped, measured against the new rule

The site landed at `2b708da`, one commit before the language rule was written.
Its user-facing content is fine — that is exactly what the rule exempts. Its
**source** is not.

| file | state |
|---|---|
| `site/build.py` | comments in Portuguese |
| `site/README.md` | entirely Portuguese |
| `site/css/styles.css`, `site/css/docs.css` | comments in Portuguese |
| `site/js/main.js`, `site/js/docs.js` | comments in Portuguese |
| `site/i18n.json` | the **keys** are Portuguese: `pular`, `nav_rotulo`, `hero_titulo`, `c1_valor` |
| `docs/COMMANDS.md` | entirely Portuguese, 643 lines |

The `i18n.json` keys are the awkward one. They are identifiers, so the rule
covers them, and they are referenced from `templates/*.html` — renaming means
touching both sides at once. It is mechanical, but it is not a find-and-replace
on one file.

`site/content/pt/docs/*.md` **stays in Portuguese**. It is the translation the
rule exists to protect.

### Spanish is promised and missing

`CONTRIBUTING.md` says the website is translated to **Portuguese, English and
Spanish**. The site ships `pt` and `en`:

```python
IDIOMAS = ["pt", "en"]   # site/build.py:30
```

The generator was built for this — adding a language is a new entry in that
list, a block in `i18n.json`, and a directory under `content/`. Nothing in the
templates or the CSS changes. What it costs is the translation of twelve pages
plus 121 UI strings, and that is the whole cost.

Until it ships, `CONTRIBUTING.md` promises something the site does not do. Either
the translation lands or that sentence is corrected — leaving both as they are
is the one option that misleads.

---

## 3. `cmd/brevis-sdk`

The SDK's CLI has not been touched since the split. Six defects, one of which
makes a documented command do nothing.

| | where | what |
|---|---|---|
| **`load` never reads stdin** | `commands.go:120` | `envelopes := []sdk.Envelope{}` with a `// TODO: read from stdin and parse NDJSON` above it. The command reports `Rows: 0` and exits clean. Its own `--help` documents the piping that does not happen. |
| **`run` only reads CSV** | `commands.go:170` | `from.HTTP{URL: url, Format: sdk.FormatCSV}` is hardcoded, and there is no `--format` flag on `run`. A JSON URL is parsed as CSV and the result is garbage. |
| **the help announces the wrong binary** | `main.go:16` | `Use: "brevis"` — the engine's name. Every usage line printed by `brevis-sdk` tells the reader to type `brevis`. |
| **the version is a literal** | `main.go:11` | `version = "0.1.0"` while `VERSION` reads `0.6.0`. No `-ldflags` path sets it. |
| **`make build` writes the wrong file** | `Makefile:17` | `-o brevis-sdk-sdk`, then echoes `./brevis-sdk`. Line 37 says `Installed: brevis`. |
| **the README links to itself** | `README.md:5` | the pointer to `cmd/brevis` goes to `../brevis-sdk/`. Line 24 tells the reader to `go work init ./sdk ./cmd/brevis`, naming a module that is not this one. |

**A decision comes before the fixes.** `load` and `run` overlap with what
`sdk.Run` already does properly in twenty lines of Go, and the SDK's own README
presents that as the path. Two options:

- **Finish the CLI.** Implement stdin, add `--format` to `run`, and it becomes a
  real way to move data without writing Go.
- **Cut it back to `extract`.** That is the one subcommand that works today and
  has no Go equivalent as a one-liner. Delete `load` and `run`, and point the
  README at `sdk.Run`.

The second is smaller and removes the two lies. The first is more product. What
is not defensible is the current state: shipping a `load` that reports success
having written nothing.

---

## 4. Repository housekeeping

| | where | what |
|---|---|---|
| **a script named in Portuguese** | `.github/scripts/peso-do-motor.sh` | filename and comments. Its three siblings are `consumer-check.sh`, `generated-check.sh`, `pruning-check.sh` — the odd one out is also the one enforcing the most interesting invariant. `engine-weight.sh` would match. |
| **a CI check that cannot fail** | `.github/workflows/build-site.yml:51` | `if grep -o 'href=…' index.html \| grep -E 'href="[0-9]' \| head -5; then` — the exit status comes from `head`, which always succeeds, so the branch always runs and prints its warning. It has been printing "potentially broken links" with zero matches. |
| **two sources of truth for the CLI** | `docs/COMMANDS.md` (643 lines) vs `site/content/{pt,en}/docs/07-cli.md` | both document the ten subcommands and their flags. They agree today because both were written from the same `--help` output; nothing keeps them agreeing. |
| **`COMMANDS.md` is unindexed** | `docs/README.md` | the root `README.md` links it twice; the docs index does not mention it. |
| **seventeen documents in Portuguese** | `docs/*.md` | outside the historical set that `CONTRIBUTING.md` explicitly exempts (`CHANGELOG*`, older `plan/`). `SDK_DECISIONS.md`, `SDK_MATRIX.md`, `KUBERNETES.md` and `IMAGES.md` are current references, not records. |

On the duplication: the website is the documentation users read, and `docs/` is
what contributors read. Keeping both means writing the CLI reference twice. The
cheaper resolution is to let the site own the user-facing reference and reduce
`docs/COMMANDS.md` to what contributors need that users do not — which today is
the "known defects" section at its end.

---

## Suggested order

Cheapest first, and each step leaves the tree in a state that ships.

1. **The two one-line fixes.** The CI `if`, and the `Makefile` output name. No
   decision needed, no compatibility cost.
2. **Decide what `cmd/brevis-sdk` is for**, then fix or cut. This is the only
   item where the software currently claims something untrue.
3. **Error messages and `Short:` strings to English.** Mechanical, no
   compatibility cost, and it removes the most visible half of item 1.
4. **The site's own source to English**, `i18n.json` keys included. Contained in
   `site/`, and the generator's `--check` proves nothing broke.
5. **Spanish**, or correct the sentence in `CONTRIBUTING.md`.
6. **The renames with a compatibility cost** — environment variables, `brand.yaml`
   keys, `brevis marca`. Last, because they need a deprecation window and a
   changelog entry, not just a commit.
7. **`docs/*.md` to English**, and resolve the CLI duplication while touching
   `COMMANDS.md` anyway.

---

## What this does not cover

This page is about **loose ends already created** — things that are inconsistent,
untrue, or promised and missing. It deliberately says nothing about new
capability: no roadmap, no features, no performance work. Those belong to the
window this inventory is meant to open, not to the one it closes.

Two things were checked and found healthy, and are recorded so they are not
re-investigated: `go test ./...` is green in both modules, and the root
`go.mod`'s `replace .../sdk => ./sdk` is inert and guarded by
`peso-do-motor.sh`, exactly as its header claims.
