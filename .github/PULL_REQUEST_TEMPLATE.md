## What changes

<!-- One sentence. The diff shows what; here, say why. -->

## Why

<!-- The problem this solves. For a bug fix: what was wrong, and since when. -->

## How it is proven

<!--
Which test would fail without this change? For a bug fix, the test IS the proof
the bug existed -- not "added tests", but which behaviour they pin down.

If you fixed something, revert your fix and check the test goes red. A test that
cannot fail is worse than no test, because it buys confidence it has not earned.
-->

## Checklist

- [ ] `go test ./...` passes at the repository root and in `sdk/`
- [ ] `golangci-lint run ./...` is clean in both modules
- [ ] Generated artefacts are up to date (`make generate`, then commit `web/`)
- [ ] Comments, identifiers and error messages are in **English** (see CONTRIBUTING.md)
