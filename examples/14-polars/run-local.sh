#!/usr/bin/env bash
# The three steps, by hand, without the engine.
#
# It exists so the example can be RUN while reading it. What the engine does for
# real is exactly this: it gives each step a BREVIS_OUTPUT to publish into, and
# hands the next step a BREVIS_INPUT holding what the steps before it published,
# keyed by step id.
#
#   cd examples && ./14-polars/run-local.sh
set -euo pipefail

cd "$(dirname "$0")/.."          # examples/

# -mod=mod, because this module `replace`s the SDK with the tree next door and
# does not pin its graph. Without it every `go run` below stops at "updates to
# go.mod needed" -- which has nothing to do with this example and is what CI
# sets for the same reason.
export GOFLAGS="${GOFLAGS:--mod=mod}"

: "${PG_DSN:=postgres://brevis:brevis@localhost:55432/brevis_it}"
export PG_DSN
export SHOP_WORKDIR="${SHOP_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/brevis-shop.XXXXXX")}"

PY="${PY:-python3}"
if ! "$PY" -c 'import polars, brevis' 2>/dev/null; then
    # Real paths, not relative ones: this message is printed after the cd
    # above, so "../lib" would be right or wrong depending on where the reader
    # happens to be standing.
    root=$(cd .. && pwd)
    cat >&2 <<MISSING
This step needs Polars and the Brevis context library, and $PY has neither:

    python3 -m venv "$root/.venv"
    "$root/.venv/bin/pip" install polars "$root/lib/python-context"

Then point PY at it:

    PY="$root/.venv/bin/python" $0
MISSING
    exit 1
fi

ctx=$(mktemp -d "${TMPDIR:-/tmp}/brevis-ctx.XXXXXX")
trap 'rm -rf "$ctx"' EXIT

# The context the engine builds: {"<step id>": {<what it published>}}. jq is not
# assumed -- Python is already required by step two.
merge() { "$PY" -c 'import json,sys; print(json.dumps({s: json.load(open(f)) for s, f in zip(sys.argv[1::2], sys.argv[2::2])}))' "$@"; }

echo "==> workdir: $SHOP_WORKDIR"

echo "==> 1/3  extract (Go)"
BREVIS_OUTPUT="$ctx/extract.json" go run ./14-polars/extract

echo "==> 2/3  shape (Polars)"
BREVIS_INPUT="$(merge extract "$ctx/extract.json")" \
    BREVIS_OUTPUT="$ctx/shape.json" "$PY" 14-polars/shape.py

echo "==> 3/3  load (Go)"
go run ./14-polars/load -create-table >/dev/null
BREVIS_INPUT="$(merge extract "$ctx/extract.json" shape "$ctx/shape.json")" \
    go run ./14-polars/load

echo
echo "Run it again: the second load reports 120 ignored, because the ingestion_id"
echo "of a snapshot is keyed on the run's date and nothing about it changed."
