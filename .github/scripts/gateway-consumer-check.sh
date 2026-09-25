#!/usr/bin/env bash
# Builds the gateway module the way a CONSUMER gets it: without the `replace`.
#
# A replace directive in a dependency's go.mod is IGNORED by the main module,
# so what a consumer actually compiles against is whatever `require` names. The
# gateway's require said sdk v0.63.0 while its code used sdk.Store, which
# landed in v0.64.0 -- and the local replace hid it completely. gateway/v0.3.1
# published, and a `go get` of it failed with `undefined: sdk.Store`.
#
# Every gate in this repository was green. Nothing built the module the way the
# outside world does, so nothing could have caught it.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cp -R "$root/gateway" "$work/gateway"
cd "$work/gateway"

# The one line that makes this a consumer's build.
go mod edit -dropreplace=github.com/AreteAcademy/brevis/sdk

sdk_version="$(go list -m -f '{{.Version}}' github.com/AreteAcademy/brevis/sdk)"
echo "building the gateway against the PUBLISHED sdk $sdk_version"

if ! GOFLAGS=-mod=mod go build ./... 2>"$work/err"; then
  echo "::error::the gateway does not build against the sdk its go.mod requires ($sdk_version)"
  echo
  sed 's/^/    /' "$work/err"
  echo
  echo "    The replace directive hides this locally. A consumer does not get it:"
  echo "    bump the sdk version in gateway/go.mod to one that has what the code uses."
  exit 1
fi

echo "✅ a consumer can build the gateway against sdk $sdk_version"
