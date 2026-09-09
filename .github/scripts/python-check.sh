#!/usr/bin/env bash
# The Python library's gates: it runs on the declared floor, and it depends on
# nothing.
#
# This lives in a script rather than inline in publish-python.yml because of
# where the two checks used to run: ONLY at publish. A commit that broke the
# library sat green on master until somebody tagged -- and by then the tag is
# pushed, the release is half done, and a PyPI filename is burned forever.
#
# That is the repository's recurring lesson from the other direction: the SDK's
# lint ran only in test.yml and two versions published red. A check has to run
# on BOTH paths, and the way to make sure the two paths check the same thing is
# for there to be one script.
#
# It found its own reason on the first run: `importlib.metadata` entered
# __init__.py after py/v0.1.1 was tagged, and the allowlist below did not have
# it. The next publish would have failed at the gate, after the tag was pushed.
set -uo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/lib/python-context"

PYTHON="${PYTHON:-python3}"
failed=0

# The DECLARED floor, not the newest. A library that only runs on 3.12 is a
# library a data team on a managed cluster cannot install, and that is the
# audience this exists for. `datetime.fromisoformat` refusing a `Z` before 3.11
# is exactly the kind of thing this catches.
echo "→ $($PYTHON -V)"
if ! $PYTHON -m pytest tests/ -q; then
  failed=1
fi

# It reads environment variables and writes a file. If that ever needs a
# dependency, something has gone wrong that a review would miss.
$PYTHON - <<'PY' || failed=1
import ast, pathlib

mods = set()
for f in pathlib.Path("src").rglob("*.py"):
    for node in ast.walk(ast.parse(f.read_text())):
        if isinstance(node, ast.Import):
            mods.update(a.name.split(".")[0] for a in node.names)
        elif isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
            mods.add(node.module.split(".")[0])

# An ALLOWLIST rather than sys.stdlib_module_names, which does not exist before
# 3.10 -- so this keeps working on the floor, and it is stricter: a new stdlib
# import is a decision somebody makes here rather than one that slips through.
#
#   importlib   __version__ is read from the installed metadata, so it cannot
#               drift from pyproject.toml
#   dataclasses
#   datetime    brevis.run: the auto params are timestamps, and handing a step
#               a string to parse would be handing back the arithmetic they
#               exist to remove
#   re          brevis.metrics validates a metric name against Prometheus's own
#               rule. A name it refuses costs the WHOLE scrape, not one series,
#               so this is checked before the line is ever written
#   sys         the marker goes to stdout, which is the pipe the engine reads
#   time        the marker carries the instant it was produced
allowed = {
    "__future__", "atexit", "dataclasses", "datetime", "importlib",
    "json", "logging", "os", "re", "sys", "tempfile", "time", "typing",
}
outside = sorted(mods - allowed)
if outside:
    print("::error::the library imports " + ", ".join(outside) +
          " -- add it to the allowlist in this script deliberately, or do not add it")
    raise SystemExit(1)
print("✅ imports only: " + ", ".join(sorted(mods)))
PY

# The version is typed in pyproject.toml and read at runtime from the installed
# metadata, so those two cannot drift. What CAN drift is pyproject against the
# tag, and publish-python.yml checks that at the moment it matters.
exit $failed
