#!/usr/bin/env bash
# Proves that a consumer only compiles what it imports.
#
# `go list -deps` resolves imports without compiling the body, so a call to a
# symbol that does not exist used to pass green -- `pycompat.Texto` did, for
# versions after Texto became Text. `go build` runs first now, so the body has
# to be real.
set -euo pipefail
TREE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../sdk" && pwd)"
MODULO="github.com/AreteAcademy/brevis/sdk"

check() {
  local name="$1" imports="$2" body="$3" forbidden="$4"
  local dir; dir="$(mktemp -d)"; trap 'rm -rf "$dir"' RETURN
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
  [ "$failed" = "0" ] && echo "✅ $name: $total packages, no foreign driver"
  return $failed
}

check "files" \
  "	\"$MODULO/from\"
	\"$MODULO/to\"" \
  "_ = from.Files{}; _ = to.Files{}" \
  "jackc/pgx cloud.google.com aws-sdk-go"

check "postgres" \
  "	\"$MODULO/from/postgres\"" \
  "_ = postgres.Query{}" \
  "cloud.google.com aws-sdk-go"

check "mysql" \
  "	\"$MODULO/from/mysql\"" \
  "_ = mysql.Query{}" \
  "jackc/pgx cloud.google.com aws-sdk-go"

# Context is the package a Python-first team's Go step imports, and it must cost
# nothing: no driver, no network, no rest of the SDK. If this ever fails, the
# package grew a dependency and a fetcher that only wanted to publish a
# watermark started paying for it.
check "context" \
  "	\"$MODULO/context\"" \
  "_ = context.MaxBytes" \
  "jackc/pgx cloud.google.com aws-sdk-go net/http"

check "pycompat" \
  "	\"$MODULO/pycompat\"" \
  "_, _ = pycompat.Text(nil)" \
  "jackc/pgx cloud.google.com aws-sdk-go net/http"

check "bigquery" \
  "	\"$MODULO/to/bigquery\"" \
  "_ = bigquery.Table{}" \
  "jackc/pgx aws-sdk-go"
