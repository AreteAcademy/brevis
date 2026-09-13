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
# Two rules, and both exist because each failed once:
#   1. Every LABEL block naming a title also names a licence.
#   2. That licence is the one in LICENSE.
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

[ "$failed" = "0" ] || exit 1
echo "✅ every image declares $want, which is what LICENSE says"
