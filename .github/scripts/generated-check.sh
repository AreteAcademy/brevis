#!/usr/bin/env bash
# Checks that web/'s generated artifacts are up to date.
#
# There are TWO generators, and the gate ran only one. `templ generate` was
# there; Tailwind was not -- despite the gate's comment claiming it covered
# `make generate`. A stale app.css passed in silence, and did: the 0.4.0 image
# shipped stamped `-dirty` because the committed CSS was out of date, and
# `make generate` dirtied the tree in the middle of publishing.
#
# Both tools are PINNED. templ comes from go.mod, which is where the library's
# version already lives -- installing @latest could generate code for a runtime
# version different from the one the binary uses. Tailwind comes from the
# variable below: `releases/latest` made two developers generate different CSS,
# and a gate that compares against a generator that changes on its own could
# never be reproducible.
set -euo pipefail

TAILWIND_VERSION="${TAILWIND_VERSION:-v4.3.3}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

templ_version="$(go list -m -f '{{.Version}}' github.com/a-h/templ)"
echo "→ templ $templ_version, tailwind $TAILWIND_VERSION"
go install "github.com/a-h/templ/cmd/templ@$templ_version"

mkdir -p bin
if [ ! -x bin/tailwindcss ] || ! ./bin/tailwindcss --help 2>&1 | head -1 | grep -q "${TAILWIND_VERSION#v}"; then
  arch="$(uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/')"
  # darwin -> macos: the release asset does not use the kernel name.
  os="$(uname -s | tr 'A-Z' 'a-z' | sed 's/darwin/macos/')"
  curl -sSLf -o bin/tailwindcss \
    "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-${os}-${arch}"
  chmod +x bin/tailwindcss
fi

"$(go env GOPATH)/bin/templ" generate
./bin/tailwindcss -i web/assets/app.src.css -o web/assets/app.css --minify

if ! git diff --exit-code -- web/; then
  echo "::error::the generated artifacts are out of date: run 'make generate' and commit web/"
  exit 1
fi
echo "✅ generated artifacts are up to date (templ and CSS)"
