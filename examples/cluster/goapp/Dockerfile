# YOUR runtime: your binary, and the agent that lets Brevis reach it.
#
# Two stages and one idea. The first builds the vendor's tool the way the
# vendor would. The second starts FROM the Brevis agent, so the image that
# comes out is a pod that (a) runs your binary and (b) answers the engine.
#
# The order matters and is the whole architecture: the agent is the FLOOR you
# build on, not a sidecar you attach. One container, one process tree, your
# tools on the PATH -- which is why a step can call `vendor` with no path, no
# volume and no init container.
#
# A real one is this file with your own build in the first stage: dbt, a
# licensed extractor, a JVM. Brevis never builds it and never updates it.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
# Static, because the agent's image is alpine and this binary is not going to
# find glibc there. The failure without it is "no such file or directory" on a
# file that is plainly present, which is the least helpful error in Linux.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vendor .

FROM brevis-agent:dev
# /usr/local/bin, so a step says `vendor info` and not a path. What a client
# copying this file changes is the line above and this one.
COPY --from=build /out/vendor /usr/local/bin/vendor
