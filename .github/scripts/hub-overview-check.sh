#!/usr/bin/env bash
# The Docker Hub overview has to FIT.
#
# Docker Hub truncates a description over 25,000 bytes, and the action that
# pushes it logs a WARNING, not an error. So docs/GATEWAY.md crossed the limit
# at gateway/v0.3.2 and the Hub page had been silently cut mid-sentence ever
# since -- nobody reads a warning in a green job.
#
# The fix was a purpose-written overview, because a Hub page and a repository
# reference are different documents for different readers. This is what keeps
# it that way.
set -euo pipefail

LIMIT=25000
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

status=0
for f in docs/GATEWAY-hub.md; do
  bytes=$(wc -c < "$root/$f" | tr -d ' ')
  if [ "$bytes" -gt "$LIMIT" ]; then
    echo "::error::$f is $bytes bytes and Docker Hub truncates at $LIMIT."
    echo "    It would be cut mid-sentence on the page everybody sees first."
    status=1
  else
    echo "✅ $f fits: $bytes of $LIMIT bytes"
  fi
done
exit $status
