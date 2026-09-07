"""Brevis context: what one step tells the next.

    from brevis import context

    bucket = context.get("extract.bucket")
    context.set(rows=48213, watermark="2026-09-07T03:00:00Z")

That is the whole library. It is not a port of the Go SDK -- no drivers, no
pagination, no ingestion ids. Python already has better tools for those, and
this package exists so a team can use them and still get the one thing an
orchestrator is for.

It has no dependencies and never will. It reads one environment variable and
writes one file.
"""

from . import context
from .context import (
    ContextError,
    NotVisible,
    TooLarge,
    get,
    of,
    published,
    set,  # noqa: A004 -- shadowing the builtin is deliberate; see context.set
)

__all__ = [
    "ContextError",
    "NotVisible",
    "TooLarge",
    "context",
    "get",
    "of",
    "published",
    "set",
]
# Read from the installed package's metadata rather than typed here.
#
# It was typed in two places -- this line and pyproject.toml -- and the publish
# gate compares the tag to pyproject only. A drift would have made `pip show`
# and `brevis.__version__` disagree in silence, which is the kind of difference
# somebody chases for an hour while debugging something else.
try:  # pragma: no cover - the fallback needs an uninstalled checkout
    from importlib.metadata import PackageNotFoundError, version as _version

    __version__ = _version("brevis")
except (ImportError, PackageNotFoundError):
    # A checkout on sys.path, with nothing installed. "devel" is the truth, and
    # telling it apart from a release is what somebody reporting odd behaviour
    # needs.
    __version__ = "devel"
