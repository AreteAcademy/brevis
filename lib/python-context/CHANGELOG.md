# Changelog — the Python library

`brevis` on PyPI, published from a `py/v*` tag. Three artifacts, three
cadences: the engine's versions are in
[`CHANGELOG-motor.md`](../../CHANGELOG-motor.md) and the Go SDK's in
[`CHANGELOG.md`](../../CHANGELOG.md). A fix here must not force an engine
release, which is why this list is its own.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
the versions follow [SemVer](https://semver.org/).

---

## [0.5.0] — 2026-09-11

> **Why 0.4.0 is missing.** It was tagged, refused by PyPI, and skipped. A
> previously deleted project under this name burned the filename
> `brevis-0.4.0.tar.gz`, and PyPI remembers filenames per project forever — the
> same ledger that took 0.2.0, 0.3.0 and 0.3.1 before it. Nothing was published
> under 0.4.0 and nothing ever can be.
>
> The release workflow uploads the **sdist first** for exactly this reason, so a
> burned name fails before anything reaches the index. That worked: this is a
> clean skip and not a half-published version.
>
> The jump to 0.5.0 rather than 0.4.1 is deliberate — the old project reached at
> least the 0.4 series, and guessing one number at a time costs a dead tag per
> guess.


### Added: `context.set()` publishes on stdout when there is no output path

An executor that runs a step on a host the engine does not manage has no
equivalent of a pod's termination message. So when `BREVIS_RUN_ID` says this is
under the engine and `BREVIS_OUTPUT` names no file, what was published goes out
as a marked line instead:

```
@brevis:{"type":"context","value":{"watermark":"2026-03-11T04:00:00Z"}}
```

The file still wins wherever there is one, and a step run by hand still prints
nothing at all. Nothing in an existing pipeline changes.

### Changed: both halves of the protocol take exactly one write

`print()` does two — the text, then the newline — and stdout is shared with
everything else a step logs, so a second thread writing between them splits the
marker across two lines. The engine's scanner breaks on lines, so what arrives
is not a marker at all. `metrics.set()` and `metrics.inc()` moved to one write
with the context publisher; the Go SDK already guaranteed this, and now they
match.

### Changed: `MARKER` lives in `brevis.context`

`brevis.metrics` imports it. A protocol literal in two files is a protocol that
changes in one of them.

---

## [0.3.2] — 2026-09-09

`0.3.0` and `0.3.1` do not exist and never will. Their filenames had belonged to
the previously deleted project called `brevis`, and PyPI's filename ledger
outlives a project forever. The ledger so far: `0.1.0`, `0.2.0`, `0.3.0` and
`0.3.1` are burned; `0.1.1`, `0.2.1` and this one are not. There is no pattern
and no API to ask — trying is the only way to find out.

**The fix from `0.2.1` is what makes trying cheap.** The sdist is uploaded
FIRST, so a 400 arrives before anything reaches the index: no wheel-only version
for `pip` to resolve, and nothing to yank. The two earlier incidents each left
half a release published; these two left nothing at all.

### Added: `brevis.metrics`

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

They come out of the **engine's** `/metrics`, labelled with the workflow and the
step:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

**This does not open a port, and it could not.** A step runs in its own pod for
forty seconds and exits; a port it opened would be scraped never, or once by
luck. The three known answers to that are a Pushgateway (a component to operate
and series that stay until deleted), an OTLP collector (a component **and** a
dependency in this library, which has none), or letting the orchestrator carry
them — and the orchestrator already reads every step's stdout for `@brevis:`
lines, and already has a meter and a Prometheus endpoint.

So the line goes to stdout and the engine records it. The labels are the
engine's because a step cannot know its own workflow slug, and asking a library
for it would get it wrong the first time somebody renamed a file.

**An invalid name is refused rather than normalised.** Prometheus accepts
letters, digits and underscore, and a name it refuses costs the **whole
scrape** — not one series. `rows-loaded` raises `MetricError` where the mistake
was made, instead of arriving under a name nobody wrote.

A counter cannot go down (every `rate()` assumes so), and a value has to be a
number — `True` is not one, even though Python says it is an `int`.

Needs the engine on `v0.12.0` or newer to be collected. On an older one the line
goes to stdout and is ignored, which is what running the script by hand does
too. Still no dependencies: `re`, `sys` and `time` joined the allowlist in
`python-check.sh`, each with its reason.

---

## [0.2.1] — 2026-09-08

Same code as `0.2.0`. That version is **wheel-only on PyPI** and should not be
used: its sdist was refused with a 400 because `brevis-0.2.0.tar.gz` had
belonged to a previously deleted project called `brevis`, and PyPI's filename
ledger outlives a project forever.

It is the second time — `0.1.0` failed the same way — so the publish workflow
now uploads the **sdist first** and the wheel second, in two steps. Everything
used to be uploaded from one directory, where the wheel goes first
alphabetically: by the time the sdist was refused, the release was already half
published and the version spent. With the sdist first, a burned filename costs
nothing, because nothing reaches the index at all.

There is no API that answers "is this filename burned", so trying is the only
way to find out. This makes trying cheap.

---

## [0.2.0] — 2026-09-08

### Added: `brevis.run`, the clock the engine already knew

The engine `v0.9.0` computes a run's
[auto params](https://brevis.dev/docs/parameters/#auto-params) and injects
them. Python now reads them the same way the Go SDK does:

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`run.now()` is the point: it is the clock to read instead of
`datetime.now()`, and on a scheduled run it is the slot, so it does not move
when the run is late and does not move when the run is retried three hours
later. A fetcher that reads `datetime.now()` and subtracts its window silently
skips exactly as much data as the queue delayed it by — and the next slot reads
its own `now()`, so nobody ever fetches the gap.

`run.window()` returns `None` rather than a pair of zero times when the
workflow has no schedule: a query from the epoch selects everything, and that
failure should not be silent.

`run.context()` carries the rest — id, first, attempt, trigger, params — and
`run.param(name, default)` reads one dispatch parameter.

### Added: `run.map_value()` and `run.map_index()`, for `for_each:`

`for_each:` shipped with the engine and this library never named the two
variables it sets, so every mapped Python step read `os.environ` by hand — and
the ones that did not know the engine **leaves them out** on an unmapped step
read `""` and carried on. Both are `None` when the step is not mapped, which
keeps "no element" distinguishable from "the element is the empty string".

A test in the engine now asserts that this library names *every* variable the
runner injects, reading the runner's own source rather than a list typed twice.
That is how this gap was found, and it is what stops the next one.

Outside the engine everything is empty, `run.now()` **is** the wall clock and
`window()` is `None`, so a script somebody runs by hand needs no special case.

Still **no dependencies**, and the gate that says so still says so. `datetime`
and `dataclasses` joined its allowlist deliberately: the auto params are
timestamps, and handing a step a string to parse would be handing back the
arithmetic they exist to remove.

### Fixed: the gates now run before the tag, not only at it

The tests and the no-dependencies check lived only in `publish-python.yml`, so
a commit that broke the library sat green on master until somebody tagged — and
by then the tag is pushed and a PyPI filename is burned forever.

It had already happened: `importlib.metadata` entered `__init__.py` after
`py/v0.1.1`, and the allowlist did not have it. The next `py/v*` tag would have
failed at the gate, mid-release.

Both paths now run the same `.github/scripts/python-check.sh`, on 3.9 and on
3.13. One script, because two copies of a check are two checks that drift.

### One detail worth naming

`datetime.fromisoformat` did not accept a `Z` suffix until 3.11, and the engine
writes RFC 3339, which always uses one. On the declared floor of 3.9 this
library would have parsed **nothing at all**, in silence, because every field
is optional and nothing would have raised. The suffix is translated before
parsing, and the test for it runs on 3.9 in CI.

---

## [0.1.1] — 2026-09-07

`0.1.0` shipped a wheel and no sdist: the wheel uploaded, the sdist was refused
because a file of that exact name had belonged to a previously deleted project,
and PyPI's filename ledger outlives the project forever. This version is the
complete release, and the publish workflow now asserts what is actually on the
index rather than trusting a step that reported success.

## [0.1.0] — 2026-09-07

`brevis.context`: read what the steps before published, publish something for
the ones after. No dependencies, one environment variable in and one file out.
