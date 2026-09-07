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
__version__ = "0.1.1"
