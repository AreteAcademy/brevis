# Images per role

Three images, each with the minimum its role needs. In Kubernetes every step is
a pod, so whatever is left over in the image is downloaded by every node that
runs that step.

**Where they live.** These three Dockerfiles are in the **data repository**,
under `brevis/imagens/`, not in this one — this repository builds the engine's
own image, which is [`PUBLISHING.md`](PUBLISHING.md). The numbers below were
measured there.

| role | size | cold start | where |
|---|---|---|---|
| **Go** | **5.8 MB** | 0.18 s | `brevis/imagens/Dockerfile.go` |
| **Python** | **118 MB** | 0.54 s | `brevis/imagens/Dockerfile.python` |
| **dbt** | **620 MB** | 3.3 s | `brevis/imagens/Dockerfile.dbt` |

Before: a single **1.87 GB** image for everything. A Go fetcher sitting next to
a `dbt build` paid 1.87 GB and the memory ceiling of the largest one — today it
pays 5.8 MB and 32Mi.

## dbt

### `dbt parse` at build time saves 2.68 s per pod

What can **not** be pre-run is `dbt build`: it runs SQL on BigQuery, and an
image build that materializes a table is an unacceptable side effect.

What can is the **parse**. `dbt parse` produces
`target/partial_parse.msgpack`, and the result travels in the image:

```
cold parse (no target/)               6.56 s
warm parse (the image's cache)        3.88 s   → 41% less
```

After that, `dbt parse` costs practically what `dbt --version` costs (3.50 s
against 3.42 s): what is left is the time to import dbt itself, which no image
removes.

### The parse needs the SAME variables as the runtime

Measured: dbt records which `env_var`s the profile used and **invalidates the
cache** when they change.

```
parse with GOOGLE_PROJECT_ID=a, run with a    3.81 s
parse with GOOGLE_PROJECT_ID=a, run with b    6.30 s   ← cache discarded
```

That is why `Dockerfile.dbt` **requires** `--build-arg GOOGLE_PROJECT_ID` and
fails the build without it. `STAGE` and `DBT_KEYFILE` (the path, not the key)
follow the same rule.

The accepted consequence: **one image per environment**. The artifact ends up
coupled to the BigQuery project, but 2.68 s per pod on every invocation pays for
that.

### No pandas, pyarrow or numpy

290 MB that `dbt-bigquery` drags in and this project does not exercise. Checked
with a real `dbt build` — two incremental models and five tests against
BigQuery, `PASS=11`.

They become necessary again if the project adopts **dbt Python models**. Until
then they are download and disk on every node in the cluster.

### What was left out, and why

- The `gcloud` CLI and `build-essential`: they belonged to the single image, not
  to dbt.
- `git`: it exists only in the build stage, for `dbt deps`. The final image does
  not reach the network at boot.
- `tini`: dbt is PID 1 and exits on its own.
- The **seeds stay** (102 MB). dbt computes their checksum during the parse;
  without them the parse fails. They sit in a layer of their own, before the
  models, because they change by the quarter while the SQL changes by the week.

## Python

A **distroless** base rather than `python:3.11-slim`: slim brings apt, dpkg,
bash and a whole filesystem the pod never uses.

The `.pyc` files **stay in the image**. It is the opposite of the "a smaller
image is always better" instinct: a pod runs once and dies, so compiling at
import time would spend CPU on every invocation to save disk that has already
been downloaded.

## Go

`FROM scratch` — the binary, the certificates and a `/tmp`. `CGO_ENABLED=0` is
what allows it: with cgo the binary would be linked against the system's libc,
which is not there.

`brevis/vendor_fake_go/`, also in the data repository, is the working example.
It talks to the BigQuery API over plain HTTP instead of using Google's library
— which would multiply the binary's size to make one call. The same choice as
the Kubernetes client inside Brevis.

## `shell: false` is required on both lean images

Neither distroless nor `scratch` has a `/bin/sh`. Without the marker on the
step, the command would go through `sh -c` and fail with "no such file or
directory" — a correct error that says nothing about the cause.

```yaml
- id: fetch
  image: registry/fetch-x:0.1.0
  shell: false
  run: python3 /app/vendor_fake/fetch.py --rows 500
```

Whoever needs a pipe, a variable or an `&&` needs a shell — and then the dbt
image (which has one) is the choice, or the line becomes a script inside the
program itself.
