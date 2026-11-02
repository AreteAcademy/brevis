#!/usr/bin/env bash
# The image's licence label has to be the licence.
#
# It was not. LICENSE has said MIT since the repository existed and the api
# image shipped `org.opencontainers.image.licenses="Apache-2.0"` -- a legal
# claim, in the metadata that Docker Scout, Syft and Grype read, wrong in every
# image ever published. The worker declared no licence at all.
#
# Nothing would have caught it: it is not prose, so repo-language-check.sh does
# not look; it is not a pin, so image-pins-check.sh does not either. It was
# found by eye while reading the file for something else, which is not a
# process.
#
# Four rules, and each exists because it failed once:
#   1. Every LABEL block naming a title also names a licence.
#   2. That licence is the one in LICENSE.
#   3. Every one names its source as "${SOURCE}", never a written URL: the
#      repository moved from AreteAcademy to brevis-sh on 2026-11-01, and
#      seven labels went on naming the old home.
#   4. Every release build that passes VERSION also passes SOURCE, so the
#      label is the repository the image was built from.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# The SPDX id from LICENSE's first line: "MIT License" -> MIT.
case "$(head -1 LICENSE)" in
  "MIT License")          want="MIT" ;;
  *"Apache License"*)     want="Apache-2.0" ;;
  *) echo "❌ cannot tell which licence LICENSE is, from: $(head -1 LICENSE)"
     echo "   teach this script the line, rather than deleting the check."
     exit 1 ;;
esac

titles="$(grep -c 'org\.opencontainers\.image\.title=' Dockerfile)"
licences="$(grep -c 'org\.opencontainers\.image\.licenses=' Dockerfile)"
failed=0

if [ "$titles" != "$licences" ]; then
  echo "❌ Dockerfile has $titles image(s) with a title and $licences with a licence"
  echo "   every published image carries the licence; the worker once did not."
  failed=1
fi

while IFS= read -r line; do
  got="$(echo "$line" | sed -E 's/.*licenses="([^"]*)".*/\1/')"
  if [ "$got" != "$want" ]; then
    echo "❌ Dockerfile declares licenses=\"$got\" and LICENSE is $want"
    failed=1
  fi
done < <(grep 'org\.opencontainers\.image\.licenses=' Dockerfile)

sources="$(grep -c 'org\.opencontainers\.image\.source="${SOURCE}"' Dockerfile)"
if [ "$titles" != "$sources" ]; then
  echo "❌ Dockerfile has $titles image(s) with a title and $sources whose source is \${SOURCE}"
  grep -n 'org\.opencontainers\.image\.source=' Dockerfile | grep -v '"${SOURCE}"' | sed 's/^/   /'
  echo "   a written URL goes stale when the repository moves; take it from the build."
  failed=1
fi

for wf in .github/workflows/*.yml; do
  versions="$(grep -c '^ *VERSION=\${{' "$wf")"
  given="$(grep -c '^ *SOURCE=\${{ github.server_url }}/\${{ github.repository }}' "$wf")"
  if [ "$versions" != "$given" ]; then
    echo "❌ $wf builds $versions image(s) with VERSION and passes SOURCE to $given"
    failed=1
  fi
done

[ "$failed" = "0" ] || exit 1
echo "✅ every image declares $want, which is what LICENSE says, and the repository it was built from"
