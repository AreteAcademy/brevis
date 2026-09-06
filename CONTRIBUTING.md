# Contributing to Brevis

## The project speaks English

Code, comments, identifiers, error messages, commit messages, tests, plans and
documentation are written in **English**. That is not a style preference: a
contributor in Berlin or Osaka reads the same comment as the person who wrote it,
and a comment nobody can read is a comment that stops being maintained.

The website's user-facing documentation is translated to Portuguese, English and
Spanish. Everything else, including this repository, is English only.

Some historical documents predate this rule and are kept as they were written —
`CHANGELOG.md`, `CHANGELOG-motor.md` and the older files in `docs/plan/`. They
are a record of decisions made on a date, and rewriting a record is not the same
as translating a project. New entries are in English.

## What a change has to carry

**A test that would fail without it.** For a bug fix, the test is the proof the
bug existed — not "added tests", but which behaviour it pins down.

**Proof that the test bites.** Revert your fix and check the test goes red. A
test that cannot fail is worse than no test: it buys confidence it has not
earned. Several tests in this repository were found to be unable to fail, and
each was found this way.

**A comment saying why, where the why is not obvious.** The diff shows what
changed. The comment exists for the person who, two years from now, will look at
a strange-looking line and want to "simplify" it. Most of the comments in this
repository name the incident that produced the line.

## Running it

```bash
go test ./...          # the engine, at the root
cd sdk && go test ./...  # the SDK, a separate module

make generate          # regenerates web/ (templ + Tailwind, both pinned)
```

Two modules, two `go.mod`. The engine does not import the SDK, and
`.github/scripts/peso-do-motor.sh` enforces it: the API and the scheduler are one
7 MB binary, and the data drivers live in the task pods.

Before opening a pull request:

```bash
golangci-lint run ./...                     # in both modules
./.github/scripts/generated-check.sh        # web/ artefacts are current
./.github/scripts/peso-do-motor.sh          # the engine stays lean
./.github/scripts/pruning-check.sh          # a consumer only compiles what it imports
```

## Things this project refuses

**A public field that does nothing.** If it is not read, it is not there.

**A number that is always zero.** It teaches people to skip the field, and then
nobody sees the one time it is real.

**Dropping data in silence.** Failing loudly is a one-line problem; a silent
divergence is a duplicate that turns up weeks later in a report.

**Guessing where the answer is not knowable.** Refusing, naming what is missing,
beats a default that is right half the time.

## The SDK's dependencies

Three, and adding a fourth needs an argument. Go prunes dependencies by package
imported, never by field used — which is why the drivers are values in `from/`
and `to/`: a fetcher that never imports the BigQuery destination never builds it.
`pruning-check.sh` is what keeps that true.
