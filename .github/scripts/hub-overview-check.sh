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

# AND IT HAS TO BE CURRENT, which fitting says nothing about.
#
# docs/GATEWAY-hub.md announced `0.7.1` in its `docker run` line and in its
# tags table while the published image was at 0.26.1 -- twenty releases, each
# one pushing that page again, none of them objecting. The size check was
# green every time, because a page can be wrong at any length.
#
# The module's own VERSION has to appear in its page. Not a parse of the
# table: the version is in the pull command too, and a reader copies that line
# before reading anything else.
status=0
for pair in "docs/GATEWAY-hub.md:gateway/VERSION" "docs/SQL-hub.md:sql/VERSION"; do
  f="${pair%%:*}"
  v="${pair##*:}"

  bytes=$(wc -c < "$root/$f" | tr -d ' ')
  if [ "$bytes" -gt "$LIMIT" ]; then
    echo "::error::$f is $bytes bytes and Docker Hub truncates at $LIMIT."
    echo "    It would be cut mid-sentence on the page everybody sees first."
    status=1
  else
    echo "✅ $f fits: $bytes of $LIMIT bytes"
  fi

  version=$(tr -d '[:space:]' < "$root/$v")
  if ! grep -qF "$version" "$root/$f"; then
    echo "::error::$f never says $version, which is what $v holds."
    echo "    That page is pushed by the release, so it would announce a"
    echo "    version nobody can pull. Update the tags table and the"
    echo "    \`docker run\` line."
    status=1
  else
    echo "✅ $f names $version"
  fi
done
exit $status
