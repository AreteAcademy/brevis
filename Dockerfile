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
RUN test -f web/assets/fonts/inter-latin.woff2 || { echo "web/assets/fonts is missing"; exit 1; }
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
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/brevis /usr/local/bin/brevis
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["brevis"]
CMD ["serve"]

# `worker` runs the workflows' `run:` steps — and that REQUIRES a shell. Running
# the scheduler on the distroless image would leave every run failing with "no
# such file or directory", which is the worst kind of error: correct and
# incomprehensible.
FROM alpine:3.20 AS worker
ARG VERSION=dev
ARG COMMIT=""
LABEL org.opencontainers.image.title="Brevis worker" \
      org.opencontainers.image.description="Brevis com shell, para executar os passos dos workflows" \
      org.opencontainers.image.source="https://github.com/AreteAcademy/brevis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
RUN apk add --no-cache ca-certificates tini
COPY --from=build /out/brevis /usr/local/bin/brevis
RUN adduser -D -u 65532 brevis
USER brevis
ENTRYPOINT ["/sbin/tini", "--", "brevis"]
CMD ["scheduler"]
