#!/usr/bin/env bash
# Proves that a consumer only compiles what it imports.
#
# `go list -deps` resolves imports without compiling the body, so a call to a
# symbol that does not exist used to pass green -- `pycompat.Texto` did, for
# versions after Texto became Text. `go build` runs first now, so the body has
# to be real.
set -uo pipefail

# The target is PINNED to linux/amd64, and the ceilings below are that
# platform's numbers.
#
# `go list -deps` otherwise resolves the stdlib for the HOST, and there are two
# axes of drift, both found by CI disagreeing with a laptop by twenty packages:
# GOOS (darwin and linux have different internals) and cgo, which on linux adds
# nineteen packages on its own through the resolver and os/user.
#
# CGO_ENABLED=0 is not a preference, it is what the Dockerfile builds with, so
# this measures what ships. linux/amd64 is the reference; the image is built for
# arm64 too, which differs by a handful of packages -- inside the headroom, and
# not worth a second set of ceilings.
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0

TREE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../sdk" && pwd)"
MODULO="github.com/AreteAcademy/brevis/sdk"

# Every case runs, and the exit status is the OR of them.
#
# With `set -e` the script stopped at the first failure, and a run that raised
# six ceilings at once reported one -- which is three more CI rounds to find out
# what the other five are.
overall=0
run() { "$@" || overall=1; }

check() {
  local name="$1" imports="$2" body="$3" forbidden="$4" ceiling="${5:-}"
  # Cleaned up explicitly rather than through `trap ... RETURN`: with `set -e`
  # gone the trap fires after the locals are popped, and "$dir: unbound
  # variable" is a confusing way to report a passing check.
  local dir; dir="$(mktemp -d)"
  cd "$dir"
  cat > go.mod <<EOF
module pruning/$name

go 1.23

require $MODULO v0.0.0

replace $MODULO => $TREE
EOF
  { echo "package main"; echo; echo "import ("; echo "$imports"; echo ")"; echo;
    echo "func main() { $body }"; } > main.go
  go mod tidy >/dev/null 2>&1
  # The build comes FIRST: `go list -deps` below resolves imports without
  # compiling, so without this a body calling a symbol that no longer exists
  # would still be counted as a passing case.
  if ! go build ./... 2>&1; then
    echo "❌ $name does not compile"
    return 1
  fi
  local deps; deps="$(go list -deps ./...)"
  local total; total="$(echo "$deps" | wc -l | tr -d ' ')"
  local failed=0
  for p in $forbidden; do
    local n; n="$(echo "$deps" | grep -c "$p" || true)"
    if [ "$n" != "0" ]; then
      echo "❌ $name compiles $n package(s) from $p, and should not"
      failed=1
    fi
  done
  # The ceiling. Until now this script PRINTED these numbers and asserted
  # nothing about them, and the gap was found the honest way: adding an OTLP
  # exporter to sdk/go.mod made `go mod tidy` pull BigQuery from 1.50 to 1.72
  # and the whole Google Cloud stack forward with it. The bigquery consumer went
  # from 456 packages to 714 and this script printed both, in green.
  #
  # That is the failure mode worth naming: package-level pruning does NOT
  # protect a consumer from a MODULE-level version bump. Nobody imported
  # anything new; the module graph moved underneath them. A count that is
  # printed and never asserted is a count nobody reads.
  if [ -n "$ceiling" ] && [ "$total" -gt "$ceiling" ]; then
    echo "❌ $name: $total packages, above the ceiling of $ceiling."
    echo "   Nothing has to be IMPORTED for this to happen -- a version bump in"
    echo "   sdk/go.mod moves the whole graph. If the growth is deliberate,"
    echo "   raise the ceiling here and say what was bought."
    failed=1
  fi
  [ "$failed" = "0" ] && echo "✅ $name: $total packages (ceiling $ceiling), no foreign driver"
  cd - >/dev/null
  rm -rf "$dir"
  return $failed
}

# same builds the SAME consumer twice -- once with `plain` as the body, once
# with `extra` -- and checks two different things about the result.
#
# The EQUALITY proves the field itself is free: declaring a Meter links nothing
# that not declaring one would not have linked.
#
# The FORBIDDEN list proves the interface's own package stayed clean, and it is
# the half with teeth. Equality alone does not: if sdk/meter.go grows an
# OpenTelemetry import, BOTH consumers pay it, the two counts stay equal, and a
# check built only on equality says nothing while every fetcher in the fleet
# grows two hundred packages. That mistake was made writing this function, found
# by reverting it, and the forbidden list is the fix.
same() {
  local name="$1" imports="$2" plain="$3" extra="$4" decls="$5" forbidden="$6"
  local a b
  a="$(deps_of "$imports" "$plain" "$decls")" || { echo "❌ $name: the plain build failed"; return 1; }
  b="$(deps_of "$imports" "$extra" "$decls")" || { echo "❌ $name: the build with it failed"; return 1; }

  local na nb failed=0
  na="$(echo "$a" | wc -l | tr -d " ")"
  nb="$(echo "$b" | wc -l | tr -d " ")"
  if [ "$na" != "$nb" ]; then
    echo "❌ $name: $na packages without it, $nb with it -- the difference is $((nb - na))"
    failed=1
  fi
  for p in $forbidden; do
    local n; n="$(echo "$b" | grep -c "$p" || true)"
    if [ "$n" != "0" ]; then
      echo "❌ $name: $n package(s) from $p reached a consumer that only declared the interface"
      failed=1
    fi
  done
  [ "$failed" = "0" ] && echo "✅ $name: $nb packages either way, nothing foreign"
  return $failed
}

# deps_of builds a throwaway consumer and prints its dependency list.
deps_of() {
  local imports="$1" body="$2" decls="$3"
  local dir; dir="$(mktemp -d)"
  {
    echo "module pruning/counted"; echo; echo "go 1.23"; echo
    echo "require $MODULO v0.0.0"; echo; echo "replace $MODULO => $TREE"
  } > "$dir/go.mod"
  { echo "package main"; echo; echo "import ("; echo "$imports"; echo ")"; echo
    echo "$decls"; echo; echo "func main() { $body }"; } > "$dir/main.go"
  (
    cd "$dir"
    go mod tidy >/dev/null 2>&1
    go build ./... >/dev/null 2>&1 || exit 1
    go list -deps ./...
  )
  local status=$?
  rm -rf "$dir"
  return $status
}

run check "files" \
  "	\"$MODULO/from\"
	\"$MODULO/to\"" \
  "_ = from.Files{}; _ = to.Files{}" \
  "jackc/pgx cloud.google.com aws-sdk-go" \
  205

run check "postgres" \
  "	\"$MODULO/from/postgres\"" \
  "_ = postgres.Query{}" \
  "cloud.google.com aws-sdk-go" \
  232

run check "mysql" \
  "	\"$MODULO/from/mysql\"" \
  "_ = mysql.Query{}" \
  "jackc/pgx cloud.google.com aws-sdk-go" \
  206

# Context is the package a Python-first team's Go step imports, and it must cost
# nothing: no driver, no network, no rest of the SDK. If this ever fails, the
# package grew a dependency and a fetcher that only wanted to publish a
# watermark started paying for it.
run check "context" \
  "	\"$MODULO/context\"" \
  "_ = context.MaxBytes" \
  "jackc/pgx cloud.google.com aws-sdk-go net/http" \
  72

run check "pycompat" \
  "	\"$MODULO/pycompat\"" \
  "_, _ = pycompat.Text(nil)" \
  "jackc/pgx cloud.google.com aws-sdk-go net/http" \
  73

# sdk.Meter has to cost NOTHING. It is an interface in the root package, and the
# implementations live in subpackages, so a fetcher that reads a CSV does not
# link a telemetry stack to publish a counter.
#
# This is asserted as an EQUALITY and not as a ceiling, because a ceiling drifts:
# the same consumer is built twice, once declaring a Meter and once not, and the
# two dependency sets have to match. If somebody moves the interface into a file
# that imports OpenTelemetry, the counts diverge and this says by how much.
run same "meter costs nothing" \
  "	\"$MODULO\"
	\"$MODULO/from\"
	\"$MODULO/to\"" \
  "_ = sdk.Pipeline{Source: sdk.Source{From: from.Files{}}, Target: sdk.Target{To: to.Files{}}}" \
  "_ = sdk.Pipeline{Source: sdk.Source{From: from.Files{}}, Target: sdk.Target{To: to.Files{}}, Meter: theMeter{}}" \
  "type theMeter struct{}
func (theMeter) Counter(string, int64, ...sdk.Attr)     {}
func (theMeter) Histogram(string, float64, ...sdk.Attr) {}" \
  "go.opentelemetry.io prometheus google.golang.org/grpc"

run check "bigquery" \
  "	\"$MODULO/to/bigquery\"" \
  "_ = bigquery.Table{}" \
  "jackc/pgx aws-sdk-go" \
  480

exit $overall
