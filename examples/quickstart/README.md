# Quickstart: a public API to a CSV, through the whole engine

A complete Brevis stack — Postgres, the API, the scheduler — running a two-step
pipeline that pulls an hourly weather forecast and folds it into one row per day.

Nothing here is a mock. The API is [Open-Meteo](https://open-meteo.com), which
needs no key; the fetchers are Go binaries built against the published SDK; and
the CSVs land in `./out` on your disk.

```bash
docker compose up -d
docker compose run --rm trigger     # queue a run now
open http://localhost:8080
```

Within about twenty seconds:

```
out/<run-id>/raw/parte-….csv      216 hourly readings
out/<run-id>/daily/parte-….csv      9 daily summaries
```

Tear it down with `docker compose down -v`.

## What you are looking at

Open the run in the UI and each step expands into the phases it went through,
with the numbers each one produced:

```
[fetch_weather] success          SDK v0.49.0
  · check      done      0ms
  · extract    done    743ms   pages=1 http_attempts=1
  · transform  done       —    in=216 out=216 dropped=0
  · load       done      6ms   rows=216 strategy=file

[daily_summary] success          SDK v0.49.0
  · extract    done      2ms   pages=1
  · transform  done       —    in=216 out=9 dropped=207 stages=3 groups=9
  · load       done      0ms   rows=9 strategy=file
```

The badge is **observed, not declared**: the SDK announces its own version from
the binary, so nothing in the YAML can make it lie. `transform` reports no
duration on purpose — it runs per record, interleaved with the read, so any
number there would be the extraction's, and would send you looking for the
bottleneck in the wrong place.

## The two steps

**`fetch-weather`** pulls the forecast, refuses a 200 that carries an error in
the body, pairs the hourly arrays by index, stamps provenance and identity, and
writes CSV.

**`summarise-weather`** reads that CSV and folds it into one row per day. Its
`Stages` are the point:

```go
sdk.Map(sdk.Accept(...), sdk.Compute("day", ...)),
sdk.Aggregate(sdk.Reduce{By: sdk.GroupBy("latitude", "longitude", "day"), ...}),
sdk.Map(sdk.IngestionID(), ...),      // last
```

Identity is derived from the row that **lands**, and after an aggregation that
row does not exist until the fold is done — so the stage that computes it runs
last. Try moving it first: the SDK refuses, naming the column, rather than
aggregating an id into a key that corresponds to nothing.

## Nothing passes a path between the steps

The second step is not told where the first one wrote. `BREVIS_RUN_ID` is
injected into every step, so both derive the same run-scoped prefix.

That also means the second step can be **retried on its own**: the raw extract
is already on disk, and a retry costs the vendor nothing.

## Two things the compose file gets right, and why

**`CGO_ENABLED=0`.** The builder is Debian and the worker is Alpine. A
dynamically linked binary lands there and fails with
`/bin/sh: /opt/brevis/bin/fetch-weather: not found` — which reads as a missing
file and is really a missing ELF interpreter.

**The scheduler exists.** Without it the stack accepts work and never runs it:
the run is created, queued, and sits there forever. The API executes nothing, by
design — whoever creates is not whoever executes.

## Making it yours

The fetchers are a plain Go module in `fetchers/`, depending only on the
published SDK. Copy the directory, change the URL and the columns, and the rest
of the stack does not need to know.
