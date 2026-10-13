#!/usr/bin/env bash
# The image sizes the documents print have to be the ones Docker Hub has.
#
# They were not, and no release noticed: the Hub page said 5.1 MB for the slim
# image and the site said 4.9 MB, for an image of 5.17 MB. Neither was stale --
# the size had barely moved since 0.25.0 -- they were two units (MB and a
# truncated MiB) and one bad rounding. One unit now, decimal MB with one
# decimal, which is what `docker pull` counts.
#
# NOT IN THE RELEASE, on purpose. release-gateway.yml checks its documents
# before the first image is pushed (see the 0.27.1 note there), and at that
# point the version being released does not exist on the Hub yet. Checking after
# the push would be a published image with a red release. Here, on every push,
# the tag in gateway/VERSION is the one already published -- except in the
# window between the release commit and its tag, which is the one case that
# only warns.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
repo="areteacademy/brevis-gateway"
version=$(tr -d '[:space:]' < "$root/gateway/VERSION")
docs=(docs/GATEWAY-hub.md
      site/content/en/docs/19-ingestion-sinks.md
      site/content/pt/docs/19-ingestion-sinks.md)

# The size Docker Hub reports for the amd64 image of a tag, in MB with one
# decimal; empty when the tag is not published.
hub_size() {
  local body code
  body=$(mktemp)
  # A network failure is an error, not a skip: a warning in a green job is
  # what nobody reads, which is how these numbers drifted in the first place.
  code=$(curl -sS --max-time 20 --retry 2 -o "$body" -w '%{http_code}' \
    "https://hub.docker.com/v2/repositories/$repo/tags/$1")
  case "$code" in
    200) jq -r '.images[] | select(.architecture == "amd64") | .size / 1e6 * 10 | round / 10' "$body" ;;
    404) ;;
    *) echo "::error::Docker Hub answered $code for $repo:$1." >&2; rm -f "$body"; exit 1 ;;
  esac
  rm -f "$body"
}

# The size in the table row of a tag: the row names it as `<tag>` or `:<tag>`,
# and its last cell is the size, with a decimal point or a decimal comma.
doc_size() {
  grep -E '^\|' "$1" | grep -E "[\`:]${2//./\\.}\`" \
    | grep -oE '[0-9]+([.,][0-9]+)? MB' | tail -1 | tr ',' '.' | cut -d' ' -f1
}

status=0
for tag in "$version" "$version-slim"; do
  want=$(hub_size "$tag")
  if [ -z "$want" ]; then
    echo "::warning::$repo:$tag is not on Docker Hub yet; nothing to compare."
    echo "    That is the window between a release commit and its tag."
    continue
  fi
  for f in "${docs[@]}"; do
    got=$(doc_size "$root/$f" "$tag")
    if [ -z "$got" ]; then
      echo "::error::$f has no size for $tag. Its table names the tag in gateway/VERSION."
      status=1
    # Equal to one decimal, not "close": 5.1 against 5.17 was the drift.
    elif [ "$(printf '%.1f' "$got")" != "$(printf '%.1f' "$want")" ]; then
      echo "::error::$f says $got MB for $tag; Docker Hub has $want MB (amd64, compressed)."
      status=1
    else
      echo "✅ $f: $tag is $got MB, as on Docker Hub"
    fi
  done
done
exit $status
