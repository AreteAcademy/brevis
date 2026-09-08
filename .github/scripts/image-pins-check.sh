#!/usr/bin/env bash
# Every manifest pins the version this tree IS.
#
# It exists because alert.yaml and report.yaml were written pinning 0.2.1 --
# copied from the manifest beside them -- and `brevis alert` does not exist in
# 0.2.1. The pod would have come up and died with "unknown command", which is a
# perfectly clear error about a file nobody would think to doubt.
#
# The same class as the node type the API and the island disagreed about: one
# number in two places, and nothing reading both.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

want="$(cat VERSION)"
failed=0

while IFS= read -r line; do
  file="${line%%:*}"
  pinned="$(echo "$line" | sed -E 's/.*daniel3843\/brevis:([0-9]+\.[0-9]+\.[0-9]+).*/\1/')"
  if [ "$pinned" != "$want" ]; then
    echo "❌ $file pins $pinned, and this tree is $want"
    failed=1
  fi
done < <(grep -rn "daniel3843/brevis:[0-9]" deployments/ examples/ || true)

if [ "$failed" = "0" ]; then
  echo "✅ every manifest pins $want"
fi
exit $failed
