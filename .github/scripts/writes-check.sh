#!/usr/bin/env bash
# No manifest in this repository unlocks writes on a warehouse.
#
# `BREVIS_SQL_SERVE_WRITES` names the connections on which `brevis-sql serve`
# will run a statement that CHANGES data. On a named connection every limit on
# that surface stands down and the credential is the only one left -- the row
# ceiling bounds a screen and the byte ceiling bounds money, and neither
# bounds damage: an UPDATE scans almost nothing and changes everything.
#
# IT IS A DEVELOPMENT SWITCH AND THE DANGEROUS EVENT IS NOT SOMEBODY USING IT.
# It is the variable surviving into a production manifest -- copied out of an
# example, inherited by a chart, left in a file nobody re-reads. A manifest in
# this repository is a thing clients copy, so a value here becomes a value
# somewhere nobody decided it.
#
# THE SAME SHAPE AS image-pins-check.sh: grep, name the file and the line,
# fail. It is deliberately not clever -- an operator who means it sets the
# variable on their own deployment, where a human decided it and no gate in
# this repository can or should object.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

VAR="BREVIS_SQL_SERVE_WRITES"
failed=0

# deployments/ is what gets applied; examples/ is what gets copied. docs/ and
# site/ are PROSE and are expected to name the variable -- that is what
# documenting it means -- so they are not read here.
while IFS= read -r line; do
  file="${line%%:*}"
  echo "❌ $file sets $VAR, and a manifest in this repository is a thing somebody copies"
  echo "    $(echo "$line" | cut -d: -f2-)"
  failed=1
done < <(grep -rn "^[^#]*${VAR}" deployments/ examples/ 2>/dev/null || true)

if [ "$failed" = "0" ]; then
  echo "✅ no manifest unlocks writes ($VAR is set by an operator, never by this repository)"
fi
exit $failed
