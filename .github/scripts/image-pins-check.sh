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
  pinned="$(echo "$line" | sed -E 's/.*areteacademy\/brevis:([0-9]+\.[0-9]+\.[0-9]+).*/\1/')"
  if [ "$pinned" != "$want" ]; then
    echo "❌ $file pins $pinned, and this tree is $want"
    failed=1
  fi
done < <(grep -rn "areteacademy/brevis:[0-9]" deployments/ examples/ || true)

# AND THE OTHER IMAGE. `areteacademy/brevis-sql` is a second published image
# with a VERSION of its own, and the loop above never saw it: it greps
# `areteacademy/brevis:[0-9]`, and `brevis-sql:` does not match that.
#
# It was added the day the first manifest pinning it was written, which is
# exactly one commit before it could have gone wrong -- alert.yaml and
# report.yaml are what happens when a pin has nobody reading it.
sqlwant="$(cat sql/VERSION)"
while IFS= read -r line; do
  file="${line%%:*}"
  pinned="$(echo "$line" | sed -E 's/.*areteacademy\/brevis-sql:([0-9]+\.[0-9]+\.[0-9]+).*/\1/')"
  if [ "$pinned" != "$sqlwant" ]; then
    echo "❌ $file pins brevis-sql $pinned, and sql/VERSION is $sqlwant"
    failed=1
  fi
done < <(grep -rn "areteacademy/brevis-sql:[0-9]" deployments/ examples/ docs/ || true)

# THE CHART'S appVersion IS THE SAME NUMBER, written somewhere `areteacademy/
# brevis:` never appears -- the template builds the tag from it.
#
# It is here because it already shipped wrong: appVersion said 0.13.0 while
# VERSION said 0.15.2, so `helm install` deployed an engine two releases behind
# and every manifest beside it was correct. Nothing read both until this.
chart="deployments/helm/brevis/Chart.yaml"
appVersion="$(sed -n 's/^appVersion: *"\{0,1\}\([0-9][0-9.]*\)"\{0,1\} *$/\1/p' "$chart")"
if [ -z "$appVersion" ]; then
  echo "❌ $chart has no appVersion this script can read"
  failed=1
elif [ "$appVersion" != "$want" ]; then
  echo "❌ $chart has appVersion $appVersion, and this tree is $want"
  failed=1
fi

if [ "$failed" = "0" ]; then
  echo "✅ every manifest pins brevis $want and brevis-sql $sqlwant, and the chart agrees"
fi
exit $failed
