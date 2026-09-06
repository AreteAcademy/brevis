# Run parameters

What changes between two runs of the same workflow without editing the file.

```yaml
name: id_verification
schedule: "0 4 * * *"

params:
  - name: load_full
    type: boolean
    default: "false"
  - name: full_refresh
    type: boolean
    default: "false"

steps:
  - id: run
    run: >-
      dbt build --vars '{"load_full":"{{ .load_full }}"}'
      {{ if eq .full_refresh "true" }} --full-refresh{{ end }}
      --select bronze_id_verification+
```

| where | how |
|---|---|
| Local CLI | `brevis run wf.yaml --param load_full=true` |
| Backfill | `brevis backfill daily --from … --to … --param load_full=true` |
| UI | a form on the workflow's page (it shows up only when there are params) |
| Cron | always the **defaults** — nobody is around to supply values at four in the morning |

The values used are written to the `runs.params` column. "What parameters did
this run with?" is the first question of any backfill investigation, and the
answer must not depend on a log.

## Types

`boolean`, `integer` and `string`. With no `type` it is `string` — the most
common one, and the only one that does not change the value's meaning.

`enum` restricts to a list; `pattern` to a regular expression. Both are
validated on the server, and the UI's form picks the control from the type (a
select for boolean and enum, a number input for integer).

## Shell injection

A param's value goes **into the step's command line**, and whoever triggers a
run is not necessarily whoever wrote the workflow. So a `string` with no
`pattern` accepts only:

```
letters  digits  _ . : / = , + @ -  space
```

Left out are quotes, `;`, `|`, `&`, `$`, backticks, parentheses and
redirections — everything the shell interprets. `--date {{ .date }}` with
`date = "; rm -rf /"` is refused before the run exists.

The set covers what this repository's real params need: dates, dbt selectors,
uids, paths, comma-separated lists. Whoever genuinely needs a character outside
it declares a `pattern:` — and then the decision is explicit, and the workflow
author's.

## Mistakes the design rules out

- **An unknown name is an error**, not silence: `--param lod_full=true` with a
  typo would run with the default and nobody would notice the backfill did not
  happen.
- **A template with a wrong name fails when the task is assembled**, naming the
  params that do exist. Without `missingkey=error`, `{{ .lod_full }}` would
  become an empty string and the command would come out silently wrong --
  `--select ` with no target.
- **An invalid default fails at publish time.** A refused default would
  otherwise only surface on the first scheduled run, in the middle of the night.

## `image:` is not templatable

On purpose. Whoever triggers a run would be choosing the image the pod runs --
that is, the code that executes. Only the command is rendered.

## Coming from Kestra

`brevis/bin/from-kestra.py` in the data repository translates `inputs:` into
`params:` and `{{ inputs.x }}` into `{{ .x }}`, conditionals included
(`x == true ? '--flag' : ''` becomes `{{ if eq .x "true" }}`). That unblocked 6
of the 10 flows that would not convert before.

Left out: Jinja's `{% if %}`, functions (`now() | dateAdd(...)`) and compound
expressions. In those cases the converter **aborts the file** rather than
emitting a command that only fails at run time.
