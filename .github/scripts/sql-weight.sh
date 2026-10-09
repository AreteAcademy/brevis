#!/usr/bin/env bash
# Proves brevis-sql stays light, and that the decision that keeps it light
# is still the one in force.
#
# The module's whole premise is weight. Its reference extractor is stdlib
# only -- 1.9 MB against a real parser's 14 to 37 -- and S5 measured the
# BigQuery client at 31 MB and 526 packages in an empty main, which is over
# the image budget before a line of brevis-sql is written. The REST endpoint
# in internal/dialect/bigquery/conn.go exists for that reason and costs
# about 200 lines, with a test for each way it could be quietly wrong.
#
# NOTHING ABOUT THAT SURVIVES A `go get`. Somebody reaches for the official
# client to fix a retry, the code gets simpler, every test passes, and the
# image triples. The forbidden list below is what makes that a conversation
# instead of a surprise.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/sql"

# Pinned to what SHIPS, for the reason engine-weight.sh gives: the image
# builds with CGO_ENABLED=0 for linux, and `go list -deps` otherwise
# resolves the stdlib for the host -- so a laptop and CI would measure
# different things and the ceiling could only be right on one of them.
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0

deps="$(go list -deps ./cmd/brevis-sql)"
total="$(echo "$deps" | wc -l | tr -d ' ')"
failed=0

# The clients this module decided NOT to carry, each with the measurement
# that decided it.
#
#   cloud.google.com/go/bigquery   31 MB, 526 packages, 236 modules, alone
#   google.golang.org/grpc         what that client is mostly made of
#   .../storage, google.golang.org/api  the same stack by another door
#   .../brevis/sdk/to/bigquery     brings all three of the above
#
# THE SDK LINE WAS SPELT WITHOUT ITS HOST and could never match. `go list
# -deps` prints `github.com/AreteAcademy/...`, the grep was anchored at
# `^AreteAcademy/...`, and the count was 0 whatever brevis-sql imported. The
# one entry guarding the biggest thing on the list was the one that had never
# run -- measured 2026-10-09 by importing the SDK and watching the gate stay
# green.
#
# It is per-DRIVER and not all-or-nothing, which is what makes it a
# conversation rather than a wall. Measured the same day, from this module:
#
#   + sdk/from/postgres     245 -> 251 packages, and nothing forbidden
#   + sdk/to/bigquery       brings cloud.google.com/go/bigquery, /storage
#                           and google.golang.org/api -- the whole list
for forbidden in \
  "cloud.google.com/go/bigquery" \
  "cloud.google.com/go/storage" \
  "google.golang.org/api" \
  "google.golang.org/grpc" \
  "github.com/AreteAcademy/brevis/sdk"
do
  n="$(echo "$deps" | grep -c "^$forbidden" || true)"
  if [ "$n" != "0" ]; then
    echo "::error::brevis-sql compiles $n package(s) from $forbidden"
    echo "    S5 measured that client at 31 MB and 526 packages in an empty"
    echo "    main, against an image budget of ${IMAGE_CEILING_MB:-30} MB. If it"
    echo "    has to come in, move the ceilings in this file in the same commit"
    echo "    and say why -- that is the whole point of the gate."
    failed=1
  fi
done

# THE PACKAGE CEILING. It does not exist to be exact; it exists so that
# growing takes a conscious decision instead of just happening.
#
# The three requires (yaml, pgx, oauth2) and what they bring.
#
# THE NUMBER DEPENDS ON THE TOOLCHAIN, and by more than it looks: 243 under
# go1.27 and 256 under the 1.26 that setup-go reads from sql/go.mod. So the
# figure to trust is CI's, and the headroom is written against that -- a
# ceiling set from a laptop's reading would be 13 packages tighter than
# whoever set it believed.
CEILING=280
if [ "$total" -gt "$CEILING" ]; then
  echo "::error::brevis-sql compiles $total packages, over the ceiling of $CEILING"
  failed=1
fi

# AND THE BINARY, because a package count is a proxy and the budget is in
# megabytes. One package can be a megabyte and twenty can be nothing.
#
# Measured with the image's own flags. The distroless static base is about
# 2 MB, so this plus two is what ships -- and the plan's budget for the
# image is 30 MB.
BINARY_CEILING_MB=16
out="$(mktemp -d)/brevis-sql"
go build -trimpath -ldflags="-s -w" -o "$out" ./cmd/brevis-sql
bytes="$(wc -c < "$out" | tr -d ' ')"
mb=$(( bytes / 1048576 ))
if [ "$mb" -gt "$BINARY_CEILING_MB" ]; then
  echo "::error::the brevis-sql binary is ${mb} MB, over the ceiling of ${BINARY_CEILING_MB} MB"
  failed=1
fi

if [ "$failed" != "0" ]; then
  exit 1
fi
echo "✅ brevis-sql: $total packages (ceiling $CEILING), ${mb} MB binary (ceiling ${BINARY_CEILING_MB} MB)"
