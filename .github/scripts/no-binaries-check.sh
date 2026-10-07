#!/usr/bin/env bash
# No tracked file is a compiled binary, and none is large enough to be one.
#
# `cmd/brevis-sdk/bravis-sdk` was committed on 2026-09-04 -- under the
# PRE-RENAME name, swept in by the commit that did the rename -- and stayed for
# a month. 30 MB in every clone, every CI checkout and every fetch, for a file
# that is rebuilt by `go build` in seconds and that nothing reads.
#
# Nothing caught it. This repository gates the engine's package count, the
# images' labels, the manifests' pins and the site's build, and had nothing
# that looked at what the repository WEIGHS.
#
# TWO RULES, because either alone has a hole:
#
#   - An EXECUTABLE is refused whatever its size. A small one is still a build
#     artifact somebody has to trust, and a binary nobody can diff is the one
#     file review cannot do its job on.
#   - A file over the CEILING is refused whatever its type, because the next
#     mistake is a 40 MB tarball or a dump, and neither is an executable.
#
# The ceiling is measured and not guessed. With the binary gone, the largest
# tracked file in this repository is CHANGELOG.md at 189 KB, and the top eight
# are all under 200 KB. One megabyte is five times the real maximum: large
# enough that no honest file meets it, small enough that nothing of this kind
# gets through.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

CEILING=$((1024 * 1024))
failed=0

while IFS= read -r f; do
  [ -f "$f" ] || continue

  case "$(file -b --mime-type "$f" 2>/dev/null)" in
    application/x-mach-binary | application/x-executable | application/x-sharedlib | application/x-dosexec)
      echo "❌ $f is a compiled binary. Build it, do not commit it"
      failed=1
      continue
      ;;
  esac

  size=$(wc -c < "$f" | tr -d ' ')
  if [ "$size" -gt "$CEILING" ]; then
    echo "❌ $f is $((size / 1024)) KB, and the ceiling is $((CEILING / 1024)) KB"
    echo "   The largest honest file in this repository is under 200 KB. If this"
    echo "   one belongs, raise the ceiling in this script and say why."
    failed=1
  fi
done < <(git ls-files)

if [ "$failed" = "0" ]; then
  echo "✅ nothing tracked is a binary, and nothing is over $((CEILING / 1024)) KB"
fi
exit $failed
