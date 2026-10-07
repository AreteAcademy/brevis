"""Say which table a step wrote, so it appears in the catalog on /data.

    from brevis import landed

    landed("bigquery://acme-prod/silver/orders", rows=len(df))
    landed("s3://acme-landing/vendors/")          # rows unknown: say nothing

One call per destination, after writing it. The Go SDK does this by itself
after every load; a Python step says it, because only the step knows what it
wrote and where.

# A target is a name, not an address

    bigquery://{project}/{dataset}/{table}
    postgres://{database}/{schema}/{table}
    redshift://{database}/{schema}/{table}
    mysql://{database}/{table}
    s3://{bucket}/{prefix}/     gs://{bucket}/{prefix}/     file:///{dir}/
    pubsub://{project}/{topic}

No host, no port, no user, no password. A host moves with a failover, and a
DSN is the string most likely to carry a credential: a catalog keyed by either
would split one table in two the day the database moved, or show a password to
everyone who can open the console. A target that looks like a DSN is refused
here, where the mistake is made.

# Absent is not zero

Leave `rows` out when the step does not know. The engine keeps "did not say",
and summing a guess into a zero would draw a table that emptied overnight.
Several calls for the same target in one run add up, so a step that writes in
batches may land per batch.

# Outside the engine

The line still goes to stdout, where it reads as what it is. Nothing collects
it and nothing fails -- the same as `metrics.set()` on a laptop.
"""

from __future__ import annotations

import json
import sys
import time
from typing import Optional

from .context import MARKER

__all__ = ["LandingError", "check_target", "landed"]

_EXAMPLE = "bigquery://project/dataset/table"

# Path segments per table scheme; object schemes take a bucket and a prefix.
_TABLE_SEGMENTS = {"bigquery": 3, "postgres": 3, "redshift": 3, "mysql": 2, "pubsub": 2}
_OBJECT_SCHEMES = {"s3", "gs", "file"}
_CEILING = 512


class LandingError(ValueError):
    """A landing this module refuses to report."""


def check_target(target: str) -> None:
    """Raise LandingError unless `target` has the shape of a target.

    The same rules as the SDK's sdk/internal/core/target.go and the engine's
    internal/domain/catalog/target.go. The three agree because they read the
    same cases: tests/targets.txt is a copy of the SDK's, and a test fails when
    it drifts.
    """

    def refuse(why: str) -> LandingError:
        return LandingError(f"target {target!r}: {why}. A target looks like {_EXAMPLE}.")

    if not isinstance(target, str) or target == "":
        raise refuse("empty")
    if len(target.encode()) > _CEILING:
        raise refuse(f"longer than {_CEILING} bytes")
    scheme, sep, rest = target.partition("://")
    if not sep:
        raise refuse("no scheme")
    table = scheme in _TABLE_SEGMENTS
    if not table and scheme not in _OBJECT_SCHEMES:
        lower = scheme.lower()
        if lower in _TABLE_SEGMENTS or lower in _OBJECT_SCHEMES:
            raise refuse("the scheme must be lower-case")
        raise refuse(f"unknown scheme {scheme!r}")
    for ch in rest:
        if ord(ch) <= 0x20 or ord(ch) == 0x7F:
            raise refuse("contains whitespace or a control character")
        if ch == "@":
            raise refuse("contains '@': a target never carries a user, so this looks like a DSN")
        if ch == "?":
            raise refuse("contains a query string")
        if ch == "#":
            raise refuse("contains a fragment")

    if table:
        segs = rest.split("/")
        want = _TABLE_SEGMENTS[scheme]
        if len(segs) != want:
            raise refuse(f"{scheme} takes {want} path segments, found {len(segs)}")
        for i, seg in enumerate(segs):
            if seg == "":
                raise refuse("an empty path segment")
            # A domain-scoped BigQuery project is `example.com:project`;
            # anywhere else a colon is a port.
            if ":" in seg and not (scheme == "bigquery" and i == 0):
                raise refuse("contains ':' -- a port is an address, not a name")
            if "*" in seg:
                raise refuse("a step lands on a table, not on a pattern")
        return

    if ":" in rest or "*" in rest:
        raise refuse("an object target takes no ':' and no pattern")
    if scheme == "file":
        if not rest.startswith("/"):
            raise refuse("a file target is an absolute path, as in file:///dir/")
        rest = rest[1:]
        if rest == "":
            return
    segs = rest.split("/")
    if scheme != "file" and segs[0] == "":
        raise refuse("no bucket")
    for i, seg in enumerate(segs):
        if seg == "" and i != len(segs) - 1:
            raise refuse("an empty path segment")


def _count(name: str, value: Optional[int]) -> Optional[int]:
    if value is None:
        return None
    # bool is an int in Python, and rows=True landing a 1 is a number whose
    # meaning depends on knowing that.
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise LandingError(
            f"{name}={value!r}: a count is a whole number of zero or more. "
            f"Leave it out when the step does not know -- absent is not zero."
        )
    return value


def landed(target: str, rows: Optional[int] = None, bytes: Optional[int] = None) -> None:  # noqa: A002
    """Tell the engine this step wrote `target`.

        landed("postgres://analytics/public/orders", rows=1200)

    Raises LandingError, and writes nothing, when the target or a count is not
    one the engine would keep.
    """
    check_target(target)
    event = {
        "type": "landed",
        "target": target,
        "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    r = _count("rows", rows)
    if r is not None:
        event["rows"] = r
    b = _count("bytes", bytes)
    if b is not None:
        event["bytes"] = b

    line = MARKER + json.dumps(event, separators=(",", ":"), sort_keys=True)
    # One write, flushed: the reasons are metrics._emit's. The engine reads the
    # pipe live, and a marker split across two writes is not a marker.
    sys.stdout.write(line + "\n")
    sys.stdout.flush()
