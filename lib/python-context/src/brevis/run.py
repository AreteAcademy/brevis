"""What the engine knows about this run, so no pipeline works it out again.

# The bug this removes

A fetcher reads ``datetime.now()``, subtracts its window and asks the vendor
for the last two hours. On a run that starts on time that is right. On a run
the queue delayed by forty minutes it is forty minutes wrong -- and those forty
minutes belong to NO run at all, because the next slot reads its own ``now()``
too. Nothing fails, nothing is logged, and the gap is found weeks later.

So the engine hands over a clock instead::

    from brevis import run

    since, until = run.window() or (run.now() - timedelta(days=1), run.now())
    df = fetch(since, until)
    df.to_parquet(f"/data/{run.auto().date}.parquet")

``run.now()`` is the SLOT on a scheduled run, so it does not move when the run
is late and does not move when the run is retried three hours later.

# How it reaches the engine

The same way the context does: it does not. The engine injects
``BREVIS_AUTO_PARAMS`` when it starts the step, and this module parses it. No
API call, no token, no port -- which is why the tests need no engine.

The engine ALSO exports one ``BREVIS_AUTO_*`` variable per value, for a shell
step with no library. This module deliberately reads only the JSON: the same
number in two places is the class of bug that outlives everyone who wrote it.

# Outside the engine

Everything is empty and ``now()`` IS the wall clock, so a script somebody runs
by hand behaves exactly as it did before this existed. There is no branch to
write for local development.
"""

from __future__ import annotations

import json
import logging
import os
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Dict, Optional, Tuple

__all__ = [
    "AutoParams",
    "RunContext",
    "auto",
    "context",
    "map_index",
    "map_value",
    "now",
    "param",
    "params",
    "window",
]

ENV_AUTO_PARAMS = "BREVIS_AUTO_PARAMS"
ENV_RUN_ID = "BREVIS_RUN_ID"
ENV_RUN_FIRST = "BREVIS_RUN_FIRST"
ENV_RUN_ATTEMPT = "BREVIS_RUN_ATTEMPT"
ENV_RUN_TRIGGER = "BREVIS_RUN_TRIGGER"
ENV_RUN_LOGICAL_DATE = "BREVIS_RUN_LOGICAL_DATE"
ENV_RUN_PARAMS = "BREVIS_RUN_PARAMS"

# A mapped step's instance. ABSENT on every unmapped step, which is most of
# them -- a variable that is always there and always empty teaches whoever
# reads the environment to ignore it. That is why these read as ``None``
# rather than as an empty string and a zero.
ENV_MAP_INDEX = "BREVIS_MAP_INDEX"
ENV_MAP_VALUE = "BREVIS_MAP_VALUE"

_log = logging.getLogger("brevis")


@dataclass(frozen=True)
class AutoParams:
    """The values the engine works out on its own.

    Every field is empty outside the engine. Use :meth:`now` rather than
    reading ``adjusted_at`` directly -- it is the same value, and it answers
    when there is no engine.
    """

    #: The slot this run represents: what the cron asked for. ``None`` on a
    #: manual run, which belongs to no slot.
    scheduled_at: Optional[datetime] = None

    #: When this ATTEMPT began. A retry moves it.
    started_at: Optional[datetime] = None

    #: The clock to read instead of ``datetime.now()``. See :meth:`now`.
    adjusted_at: Optional[datetime] = None

    #: How late this attempt was against its slot. ``0`` on a manual run, and
    #: never negative. Here so nobody subtracts two timestamps to find out.
    delay_seconds: int = 0

    #: The window this run covers, end EXCLUDED. ``None`` when the workflow has
    #: no schedule. See :meth:`window`.
    interval_start: Optional[datetime] = None
    interval_end: Optional[datetime] = None

    #: The run before this one did not succeed.
    previous_error: bool = False

    #: The slot of the last run that DID succeed, which is what makes
    #: ``previous_error`` actionable: a boolean says something is wrong, a
    #: timestamp says how far back to catch up.
    previous_success_at: Optional[datetime] = None

    #: ``adjusted_at`` as ``YYYY-MM-DD``, in UTC: the partition, the folder,
    #: the ``WHERE``. Empty outside the engine -- use ``now().date()`` if you
    #: need one either way.
    date: str = ""

    def now(self) -> datetime:
        """The time this run should read, always timezone-aware and in UTC.

        On a scheduled run it is the slot; on a manual one, when the run
        started. Outside the engine it is the wall clock.
        """
        if self.adjusted_at is None:
            return datetime.now(timezone.utc)
        return self.adjusted_at

    def window(self) -> Optional[Tuple[datetime, datetime]]:
        """``(start, end)`` for this run, end excluded, or ``None``.

        ``None`` and not a pair of zero times: a workflow with no schedule has
        no window, and querying from the epoch selects everything. The caller
        has to decide what to do instead, which is why this says so.
        """
        if self.interval_start is None or self.interval_end is None:
            return None
        return self.interval_start, self.interval_end

    @property
    def from_engine(self) -> bool:
        """Whether anything was injected at all."""
        return self.adjusted_at is not None


@dataclass(frozen=True)
class RunContext:
    """Everything the engine injected, of which :attr:`auto` is the useful part."""

    #: The run's id, for tying a log line to a row in the engine's history.
    id: str = ""

    #: True when no earlier attempt of this step has succeeded. The engine
    #: decides it, because only the engine has the history.
    first: bool = False

    #: This step's attempt, counting from zero.
    attempt: int = 0

    #: ``schedule``, ``manual`` or ``backfill``.
    trigger: str = ""

    #: The slot, as the engine's own ``BREVIS_RUN_LOGICAL_DATE``. It is the
    #: same instant as ``auto.scheduled_at``; prefer ``auto.now()``, which also
    #: answers on a manual run.
    logical_date: Optional[datetime] = None

    #: What a HUMAN passed for this run. Never ``None``.
    params: Dict[str, str] = field(default_factory=dict)

    #: This instance's position and element under ``for_each:``, or ``None``
    #: on a step that is not mapped. See :func:`map_value`.
    map_index: Optional[int] = None
    map_value: Optional[str] = None

    #: What nobody had to pass.
    auto: AutoParams = field(default_factory=AutoParams)

    @property
    def from_engine(self) -> bool:
        """Whether this process is running as a Brevis step."""
        return bool(self.id)


# --------------------------------------------------------------------------
# The front door
# --------------------------------------------------------------------------


def auto() -> AutoParams:
    """The run's automatic params.

    Read on every call rather than cached at import: a module that snapshots
    the environment at import time is a module whose behaviour depends on
    import order, and no test can set a variable after it.
    """
    raw = os.environ.get(ENV_AUTO_PARAMS, "").strip()
    if not raw:
        return AutoParams()
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError as exc:
        _log.warning("ignoring malformed %s: %s", ENV_AUTO_PARAMS, exc)
        return AutoParams()
    if not isinstance(parsed, dict):
        _log.warning("%s is not a JSON object; ignoring it", ENV_AUTO_PARAMS)
        return AutoParams()

    return AutoParams(
        scheduled_at=_moment(parsed.get("scheduled_at")),
        started_at=_moment(parsed.get("started_at")),
        adjusted_at=_moment(parsed.get("adjusted_at")),
        delay_seconds=_whole(parsed.get("delay_seconds")),
        interval_start=_moment(parsed.get("interval_start")),
        interval_end=_moment(parsed.get("interval_end")),
        previous_error=bool(parsed.get("previous_error", False)),
        previous_success_at=_moment(parsed.get("previous_success_at")),
        date=str(parsed.get("date") or ""),
    )


def now() -> datetime:
    """The clock this run should read, instead of ``datetime.now()``.

    Shorthand for ``auto().now()``, because it is the call that matters and a
    step should not have to know the type's name to make it.
    """
    return auto().now()


def window() -> Optional[Tuple[datetime, datetime]]:
    """``(start, end)`` for this run, end excluded, or ``None``.

    Shorthand for ``auto().window()``::

        since, until = run.window() or (run.now() - timedelta(days=1), run.now())
    """
    return auto().window()


def context() -> RunContext:
    """Everything the engine injected about this execution."""
    return RunContext(
        id=os.environ.get(ENV_RUN_ID, ""),
        first=os.environ.get(ENV_RUN_FIRST) == "true",
        attempt=_whole(os.environ.get(ENV_RUN_ATTEMPT)),
        trigger=os.environ.get(ENV_RUN_TRIGGER, ""),
        logical_date=_moment(os.environ.get(ENV_RUN_LOGICAL_DATE)),
        params=params(),
        map_index=map_index(),
        map_value=map_value(),
        auto=auto(),
    )


def params() -> Dict[str, str]:
    """The values this run was dispatched with. Never ``None``."""
    raw = os.environ.get(ENV_RUN_PARAMS, "").strip()
    if not raw:
        return {}
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError as exc:
        _log.warning("ignoring malformed %s: %s", ENV_RUN_PARAMS, exc)
        return {}
    if not isinstance(parsed, dict):
        _log.warning("%s is not a JSON object; ignoring it", ENV_RUN_PARAMS)
        return {}
    return {str(k): str(v) for k, v in parsed.items()}


def param(name: str, default: str = "") -> str:
    """One dispatch parameter.

    An absent parameter is not an error: the workflow's ``params:`` block has
    defaults of its own, and a step that wants one anyway can pass ``default``.
    """
    return params().get(name, default)


def map_index() -> Optional[int]:
    """This instance's position under ``for_each:``, or ``None``.

    ``None`` and not ``-1``: a step that is not mapped has no position, and a
    sentinel is a number somebody eventually does arithmetic on.
    """
    raw = os.environ.get(ENV_MAP_INDEX)
    if raw is None or raw == "":
        return None
    return _whole(raw)


def map_value() -> Optional[str]:
    """The element this instance was given under ``for_each:``, or ``None``.

        partition = run.map_value()
        if partition is None:
            raise SystemExit("this step is meant to run under `for_each:`")

    A JSON string arrives WITHOUT its quotes, because ``for_each`` over
    ``["2026-01"]`` should hand a step ``2026-01`` and not ``"2026-01"``. That
    is the engine's rule, and it is the useful one: the common case needs no
    parsing at all.

    Anything else -- an object, a number, a list -- arrives as its JSON, so a
    step that maps over objects parses it::

        item = json.loads(run.map_value())

    ``None`` means the step is not mapped, which is a different thing from an
    element that happens to be the empty string. The engine leaves the variable
    out rather than setting it empty, so the two stay distinguishable.
    """
    return os.environ.get(ENV_MAP_VALUE)


# --------------------------------------------------------------------------
# Parsing
# --------------------------------------------------------------------------


def _moment(value: Any) -> Optional[datetime]:
    """Parse one RFC 3339 timestamp, or give up quietly.

    ``datetime.fromisoformat`` did not accept a ``Z`` suffix until 3.11, and
    the engine writes RFC 3339, which always uses one. On the declared floor of
    3.9 the whole library would parse nothing at all -- silently, since every
    field is optional. That is why the suffix is translated here rather than
    left to the standard library, and why there is a test for a timestamp with
    a ``Z`` in it.
    """
    if not isinstance(value, str) or not value:
        return None
    text = value[:-1] + "+00:00" if value.endswith(("Z", "z")) else value
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        _log.warning("ignoring malformed timestamp %r from %s", value, ENV_AUTO_PARAMS)
        return None
    # A naive timestamp means whoever wrote it dropped the offset. UTC is what
    # the engine writes, and guessing the local zone here would make the same
    # run read differently on a laptop and in a pod.
    if parsed.tzinfo is None:
        return parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def _whole(value: Any) -> int:
    if value is None or value == "":
        return 0
    try:
        return int(value)
    except (TypeError, ValueError):
        _log.warning("ignoring malformed integer %r", value)
        return 0
