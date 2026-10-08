# syntax=docker/dockerfile:1.7
#
# A static binary in a distroless image. Unlike Leoflow's task image, which
# needed Python and bash for its agent, here the process IS the binary — there is
# no shell to execute, so distroless is both possible and desirable.

# BUILDPLATFORM: it ALWAYS compiles on the builder's native architecture and
# cross-compiles to the target. Without it, an arm64 build on an amd64 runner
# runs under QEMU emulation and takes minutes instead of seconds.
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# The CSS is embedded in the binary (web/assets/embed.go). It is versioned in the
# repo precisely so the image does not need Tailwind; if it disappears, the
# //go:embed fails here, in the build, and not in production with an unstyled
# page.
RUN test -f web/assets/app.css || { echo "web/assets/app.css is missing — run 'make generate'"; exit 1; }
# The fonts and the UMD bundles are served from the binary. Without them the UI
# loads, but with the system's typography and a blank DAG screen — silent
# failures that only show up in the browser. Failing here, with a message, is
# better.
#
# The fonts are checked against what app.css ASKS FOR rather than against a
# filename typed here. A typed one is the same font name in two places, and it
# broke exactly that way: the typeface changed from Inter to IBM Plex, this
# line kept naming `inter-latin.woff2`, and the release build failed after the
# tag was already pushed. Reading the stylesheet cannot go stale.
RUN set -eu; fonts=$(grep -oE 'url\([^)]*/assets/fonts/[^)]*\)' web/assets/app.css | sed -E 's|.*/assets/||; s|[")]||g' | sort -u); test -n "$fonts" || { echo "app.css asks for no font at all"; exit 1; }; for f in $fonts; do test -f "web/assets/$f" || { echo "app.css asks for web/assets/$f and it is not here"; exit 1; }; done; echo "fonts: $(echo "$fonts" | wc -w) present"
RUN test -f web/assets/vendor/xyflow.js || { echo "web/assets/vendor is missing"; exit 1; }

# The version stamped into the binary. `brevis version` inside the container is
# the only reliable way to know what is running when the image's tag has been
# moved.
ARG VERSION=dev
ARG COMMIT=""
ARG BUILD_DATE=""

# TARGETOS/TARGETARCH come from buildx. Without them, a multi-arch build would
# compile everything for the builder's architecture and the arm64 image would
# carry an amd64 binary — which only fails on the cluster's first `docker run`.
ARG TARGETOS
ARG TARGETARCH

# -trimpath e -s -w tiram caminhos absolutos e tabelas de debug.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
      -o /out/brevis ./cmd/brevis

# The agent, which is the other half of `host:`.
#
# Built here for the reason the gateway is: it shares this stage's cache and
# its ldflags, and the artifacts stay apart. It is a different PROGRAM from the
# engine -- nothing in the engine imports internal/agent -- and it ships as its
# own image below.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
      -o /out/brevis-agent ./cmd/brevis-agent

# The gateway, which is a DIFFERENT binary from a different module.
#
# It is separate for the reason engine-weight.sh states: the engine orchestrates
# and never touches customer data, and the gateway does nothing else. Building
# it here rather than in a Dockerfile of its own is only so the two share this
# one's build cache and its ldflags; the artifacts stay apart.
#
# `-mod=mod`, because the module `replace`s the SDK with the tree next door and
# does not pin its graph -- the same reason the examples need it.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd gateway && GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT}" \
      -o /out/brevis-gateway ./cmd/gateway \
    && mkdir -p /out/dead-letter

# And the slim gateway, which is the SAME package with a different main.
#
# Two sinks -- postgres and a local `files` dead letter -- instead of six, and
# no object stores. That is 10 MB against 49, and the difference is entirely
# drivers a deployment with those two will never call: the AWS SDK, the Google
# client stack, Arrow.
#
# Built here rather than in an image of its own so both share this cache, and
# because they must be built from ONE tree: two images from two checkouts is
# how a `-slim` tag ends up one commit behind the tag it claims to match.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd gateway && GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT}" \
      -o /out/brevis-gateway-slim ./cmd/gateway-slim

# brevis-sql: a THIRD module and a third binary, for the rule #66 states --
# anything holding a warehouse driver is its own module and binary, so the
# engine's weight gate stays where it is.
#
# Built in this stage for the same reason the gateway is: it shares the
# cache and the ldflags, and two images from two checkouts is how a tag
# ends up one commit behind the tag it claims to match. The artifacts stay
# apart.
#
# `-mod=mod` for the reason the gateway needs it: the module does not pin
# its graph.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd sql && GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT}" \
      -o /out/brevis-sql ./cmd/brevis-sql

# Two images from the SAME binary, because the two roles have opposite
# requirements.
#
# `api` only serves HTTP: it runs nothing, so distroless (no shell, minimal
# surface) is both possible and desirable.
FROM gcr.io/distroless/static-debian12:nonroot AS api
ARG VERSION=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Brevis" \
      org.opencontainers.image.description="A data orchestration and transformation engine" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/brevis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/brevis /usr/local/bin/brevis
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["brevis"]
CMD ["serve"]

# brevis-sql, distroless: it connects to a warehouse and runs SQL. There is
# no shell to execute and nothing to exec, so distroless is both possible
# and desirable -- the same argument the `api` target makes above.
#
# ITS WEIGHT IS A GATE, not a hope: .github/scripts/sql-weight.sh refuses
# the official BigQuery client by name and caps the binary, because the
# module's whole premise is that it does not carry one. The budget for this
# image is 30 MB and it is about 13.
FROM gcr.io/distroless/static-debian12:nonroot AS sql
ARG VERSION=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="brevis-sql" \
      org.opencontainers.image.description="Plain .sql models become tables and views, in dependency order" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/brevis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/brevis-sql /usr/local/bin/brevis-sql
USER nonroot:nonroot
ENTRYPOINT ["brevis-sql"]
CMD ["compile"]

# `worker` runs the workflows' `run:` steps — and that REQUIRES a shell. Running
# the scheduler on the distroless image would leave every run failing with "no
# such file or directory", which is the worst kind of error: correct and
# incomprehensible.
FROM alpine:3.20 AS worker
ARG VERSION=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Brevis worker" \
      org.opencontainers.image.description="Brevis with a shell, for running a workflow's steps" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/brevis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates tini
COPY --from=build /out/brevis /usr/local/bin/brevis
RUN adduser -D -u 65532 brevis
USER brevis
ENTRYPOINT ["/sbin/tini", "--", "brevis"]
CMD ["scheduler"]

# The agent: alpine, and the reason is the same one the worker has.
#
# ITS WHOLE JOB IS TO RUN A COMMAND. Its default shell is `/bin/sh -c`, so a
# distroless agent is an agent that can run nothing -- it would start, answer
# the engine, and fail every step with "no such file or directory". The api and
# the gateway are distroless because they execute nothing; this one is the
# opposite case.
#
# What a deployment adds on top is the step's own runtime -- dbt, a Python, a
# licensed binary -- because that is the point of `host:`: the pod is yours and
# it already has what it needs. This image is the floor, not the ceiling.
FROM alpine:3.20 AS agent
ARG VERSION=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Brevis agent" \
      org.opencontainers.image.description="Runs Brevis steps on a host the engine does not manage" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/brevis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates tini
COPY --from=build /out/brevis-agent /usr/local/bin/brevis-agent
RUN adduser -D -u 65532 brevis
USER brevis
# tini for the same reason the worker has it: this process spawns a shell which
# spawns whatever the step is, and PID 1 without a reaper leaves zombies behind
# on a long-lived pod -- which is exactly what this image is for.
ENTRYPOINT ["/sbin/tini", "--", "brevis-agent"]
# No default CMD. Every flag that matters -- the token, the secrets directory,
# the allowlist, the advertised address -- is a decision for the deployment,
# and a default here would be one of them made silently.

# `gateway` is its own image: it holds the gateway binary and nothing else.
#
# Distroless, like the api and for the same reason: it serves HTTP and executes
# nothing, so it needs no shell. The config is a file the operator mounts --
# never baked in, because a gateway that has to be rebuilt to change a flush
# interval is a gateway nobody tunes.
FROM gcr.io/distroless/static-debian12:nonroot AS gateway
ARG VERSION=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Brevis gateway" \
      org.opencontainers.image.description="An HTTP endpoint that lands data" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/brevis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/brevis-gateway /usr/local/bin/brevis-gateway

# The dead letter's directory, owned by the user that writes it.
#
# A named volume inherits the ownership of the image path it is first mounted
# over. Without this the volume arrives owned by root, the process is nonroot,
# and the dead letter fails with `mkdir: permission denied` -- at the exact
# moment it is needed, which is after a sink has already refused. Found by
# pointing a gateway at a topic that does not exist and reading the log.
COPY --from=build --chown=nonroot:nonroot /out/dead-letter /var/dead-letter

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["brevis-gateway"]
CMD ["/etc/brevis/gateway.yaml"]

# `gateway-example` is the gateway docker-compose runs: gateway/example, the
# same package with the example's hooks compiled in.
#
# The published image builds ./cmd/gateway, which registers no hooks -- a hook
# is Go, and a published binary cannot carry somebody else's. The local config
# docker-compose mounts names `enrich_clicks`, which is what puts `tenant` in
# the payload docs/GATEWAY.md shows, and only gateway/example registers it. With
# the published binary the local gateway restarted in a loop on a hook it did
# not have (issue #68), from the moment the image moved to ./cmd/gateway.
#
# A stage of its own, so BuildKit builds it only when this target is asked
# for: the release images never compile the example.
FROM build AS build-gateway-example
# An ARG does not cross a FROM: redeclared, or the ldflags and the target
# platform would arrive empty.
ARG VERSION=dev
ARG COMMIT=""
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd gateway && GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT}" \
      -o /out/brevis-gateway-example ./example

FROM gcr.io/distroless/static-debian12:nonroot AS gateway-example
COPY --from=build-gateway-example /out/brevis-gateway-example /usr/local/bin/brevis-gateway
COPY --from=build --chown=nonroot:nonroot /out/dead-letter /var/dead-letter
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["brevis-gateway"]
CMD ["/etc/brevis/gateway.yaml"]

# `gateway-slim` is the gateway with two sinks instead of six.
#
# Postgres and a local `files` dead letter, which is the shape most deployments
# actually have: events arrive over HTTP and land in a table, and what the table
# will not take goes to a mounted volume.
#
# 10 MB against the full image's 49. A config naming `bigquery` here is refused
# AT STARTUP, naming what this binary carries -- "not one this binary carries
# (it has: files, postgres)" -- which is the honest message, because it is a
# build that left it out rather than a destination that does not exist.
#
# Want a different pair? cmd/gateway-slim is fifteen lines and the import list
# is the whole configuration. Anybody with a hook is compiling their own binary
# already.
FROM gcr.io/distroless/static-debian12:nonroot AS gateway-slim
ARG VERSION=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Brevis gateway (slim)" \
      org.opencontainers.image.description="An HTTP endpoint that lands data: Postgres and a local dead letter" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/brevis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/brevis-gateway-slim /usr/local/bin/brevis-gateway

# The same volume ownership the full image needs, and for the same reason: a
# named volume inherits the ownership of the path it is first mounted over, and
# without this the dead letter fails with `mkdir: permission denied` at the one
# moment it matters.
COPY --from=build --chown=nonroot:nonroot /out/dead-letter /var/dead-letter

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["brevis-gateway"]
CMD ["/etc/brevis/gateway.yaml"]
