# GitHub Actions workflows

CI/CD for Brevis. Six workflow files plus Dependabot.

## The workflows

### 1. `test.yml` — Test & Lint

Runs on `push` (main, master, develop) and on `pull_request`. Six jobs:

| job | what it does |
|---|---|
| **Test** | the SDK's tests, with coverage sent to Codecov |
| **Engine** | the engine's own: gofmt, build, vet, `-race`, `go mod tidy`, the generated artifacts, the engine's weight, golangci-lint |
| **Integration** | the tests that need a real service: MinIO, Postgres, MySQL, and the engine end to end. BigQuery and GCS run only when `GCP_CREDENTIALS` exists, and the job says so out loud when they do not |
| **Lint** | gofmt, go vet, go mod tidy, golangci-lint on the SDK |
| **Security Scan** | Gosec, with the result in GitHub's Security tab |
| **Build** | builds and tests the SDK, the engine, the SDK's CLI and the examples |

The **Engine** job exists because there was none: every other job in this file
does `cd sdk`, and the only `go test ./...` at the root lived in the release
gate. Since the release had never run, the engine reached v0.4.0 without a test
having run outside the machine of whoever wrote it.

### 2. `release.yml` — publish the engine's images

Runs on a `v*` tag, and on `workflow_dispatch` with a `version` input.

It runs the tests and the generated-artifacts check, refuses the tag when
`VERSION` does not match it, and publishes two multi-arch images
(`linux/amd64` and `linux/arm64`) to Docker Hub: the distroless one for the API
and the alpine one, with a shell, for the worker.

The VERSION check is what stops `brevis version` inside the image from lying
about the tag that published it — and that is the field an incident is traced
by.

Details in [`docs/PUBLISHING.md`](../docs/PUBLISHING.md).

### 3. `publish-sdk.yml` — publish the SDK

Runs on a `sdk/v*` tag. Five jobs, in order:

| job | what it does |
|---|---|
| **Validate SDK Tag** | the tag has to be valid semver |
| **Run Tests Before Publishing** | the tests, the lint, the per-driver pruning, and a clean consumer compiled against this tree |
| **Create GitHub Release** | creates the release |
| **Notify pkg.go.dev** | warms the Go proxy |
| **Verify Publication** | builds a real consumer against the **published** module |

Everything that can refuse runs BEFORE the tag exists, and that ordering is the
point: once the proxy has stored a version it is immutable forever. The gate
used to run only the tests, and a tree with a red lint published two versions
before anybody looked; the pruning check lived only in `test.yml`, and v0.41.0
published with a dependency regression while Integration went red in parallel.

```bash
git tag sdk/v0.52.0
git push origin sdk/v0.52.0
```

### 4. `build-site.yml` — build and deploy the site

Runs on `push` to main and on `pull_request`, when `site/` changes. It validates
the HTML, the CSS and the links, runs Lighthouse CI on pull requests, and builds
and pushes the site's Docker image on main.

### 5. `release-notes.yml` — generate the release notes

Runs on `release`. It extracts the commits since the previous version and
updates the GitHub Release's body.

### 6. `quality.yml` — code quality

Runs on `push` to main and on `pull_request`. gofmt, go vet, and a coverage
figure with a badge updated on main.

### 7. `dependabot.yml` — dependency updates

Weekly, Mondays: the SDK's Go dependencies at 03:00 UTC and the GitHub Actions
at 04:00. It opens at most 5 pull requests at a time and labels them
`dependencies`, `go` and `ci`.

## Triggers

| workflow | push | PR | release | tag |
|---|---|---|---|---|
| `test.yml` | ✅ | ✅ | — | — |
| `release.yml` | — | — | — | ✅ (`v*`) |
| `publish-sdk.yml` | — | — | — | ✅ (`sdk/v*`) |
| `build-site.yml` | ✅ (`site/`) | ✅ (`site/`) | — | — |
| `release-notes.yml` | — | — | ✅ | — |
| `quality.yml` | ✅ | ✅ | — | — |
| `dependabot.yml` | — | — | — | weekly |

## The secrets

Configured under `Settings → Secrets and variables → Actions`.

| secret | needed by | for what |
|---|---|---|
| `DOCKERHUB_USERNAME` | `release.yml` | publishing the engine's images |
| `DOCKERHUB_TOKEN` | `release.yml` | a Docker Hub token with *Read & Write*, never the password |
| `DOCKER_USERNAME` | `build-site.yml` | publishing the site's image |
| `DOCKER_PASSWORD` | `build-site.yml` | idem |
| `GCP_CREDENTIALS` | `test.yml` | the BigQuery and GCS integration. Without it those tests skip, and the job says which ones did not run |
| — | `test.yml` | Codecov runs without a token (a public repository) and with `fail_ci_if_error: false`, so an outage there does not turn the run red |

The two pairs are different on purpose: the engine's images and the site's
images go to different repositories, and one credential should not be able to
publish both.

## The four gates

They run in CI and are runnable by hand, which is what keeps them useful:

```bash
./.github/scripts/generated-check.sh   # web/'s templ and CSS are up to date
./.github/scripts/engine-weight.sh     # the engine compiles no data driver
./.github/scripts/pruning-check.sh     # a consumer only compiles what it imports
./.github/scripts/consumer-check.sh local
```

Each exists because something got past without it. The reasons are written at
the top of each script.

## Status badges

```markdown
[![Tests](https://github.com/AreteAcademy/brevis/actions/workflows/test.yml/badge.svg?branch=master)](https://github.com/AreteAcademy/brevis/actions/workflows/test.yml)
[![SDK Version](https://img.shields.io/github/v/tag/AreteAcademy/brevis?filter=sdk/*&label=SDK)](https://github.com/AreteAcademy/brevis/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/AreteAcademy/brevis/sdk)](https://goreportcard.com/report/github.com/AreteAcademy/brevis/sdk)
```

## Debugging a red run

```bash
gh run list --workflow=test.yml --limit 5
gh run view <id> --log-failed
```

The four gates reproduce locally with the commands above, which is the fastest
path: a gate that only runs in CI is a gate you debug by pushing.
