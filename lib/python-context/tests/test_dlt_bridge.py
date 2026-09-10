"""The bridge from Brevis's run window to dlt's incremental cursor.

There is no Brevis code in this file, and that is the point. The bridge is two
environment variables -- `DLT_INTERVAL_START` and `DLT_INTERVAL_END` -- which
the engine sets from the run's window and dlt reads in
`dlt/extract/incremental/context.py` before it goes looking for Airflow. No
library sits between them.

Which is exactly why it needs a test. Nothing compiles both sides: dlt could
rename the variables in a minor release, or the engine could change the format,
and a pipeline would quietly go back to reading its own persisted cursor. That
does not raise. It loads the wrong window, and it is found weeks later in a row
count nobody can explain.

The environment comes from `engine_dlt_env.json`, which the ENGINE writes
(internal/domain/run/autoparams_test.go). Retyping the values here would prove
that two strings in this repository match each other.

SKIPPED without dlt, and CI does not install it -- the library this package
ships depends on nothing and is not about to grow a dependency for a test. Run
it against a real dlt when touching either side:

    pip install dlt && pytest tests/test_dlt_bridge.py
"""

import json
import os
import pathlib
from datetime import datetime, timezone

import pytest

dlt = pytest.importorskip("dlt", reason="the bridge test needs a real dlt")

FIXTURE = pathlib.Path(__file__).parent / "engine_dlt_env.json"


@pytest.fixture
def engine_env(monkeypatch):
    """The environment the engine really hands a step, from the engine's file."""
    env = json.loads(FIXTURE.read_text())
    for name, value in env.items():
        monkeypatch.setenv(name, value)
    return env


def _at(text):
    return datetime.fromisoformat(text.replace("Z", "+00:00"))


def _rows(window_start, window_end):
    """Five rows placed AROUND the window, including both boundaries.

    The two boundary rows are the ones with teeth: a bridge that got the window
    roughly right would still load them, and half-open is the whole reason
    consecutive runs neither gap nor overlap.
    """
    day = window_end - window_start
    return [
        {"id": 1, "updated_at": window_start - day},          # before
        {"id": 2, "updated_at": window_start},                # the start: INCLUDED
        {"id": 3, "updated_at": window_start + day / 2},      # inside
        {"id": 4, "updated_at": window_end},                  # the end: EXCLUDED
        {"id": 5, "updated_at": window_end + day},            # after
    ]


def _resource(rows, seen):
    @dlt.resource(name="events")
    def events(
        updated_at=dlt.sources.incremental(
            "updated_at",
            # A datetime, not a string. dlt refuses to join an external
            # scheduler on a `text` cursor -- it will not coerce for
            # comparison -- and the error names JoinSchedulerError, which is
            # not obviously about this. Worth failing here rather than in
            # somebody's pipeline.
            initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
            allow_external_schedulers=True,
        )
    ):
        seen["initial_value"] = updated_at.initial_value
        seen["end_value"] = updated_at.end_value
        yield rows

    return events


def test_dlt_binds_to_the_window_the_engine_handed_it(engine_env):
    start = _at(engine_env["DLT_INTERVAL_START"])
    end = _at(engine_env["DLT_INTERVAL_END"])
    seen = {}

    loaded = [row["id"] for row in _resource(_rows(start, end), seen)]

    assert seen["initial_value"] == start, (
        "dlt did not take its lower bound from DLT_INTERVAL_START"
    )
    assert seen["end_value"] == end, (
        "dlt did not take its upper bound from DLT_INTERVAL_END"
    )
    # The start is in, the end is out. Both sides call it half-open; this is
    # the assertion that would notice if either stopped.
    assert loaded == [2, 3], (
        f"the window is not [start, end): loaded {loaded}, wanted [2, 3]"
    )


def test_an_older_slot_reads_its_own_window_and_leaves_the_cursor_alone(
    engine_env, monkeypatch
):
    """The property that makes a backfill correct and a retry idempotent.

    dlt's own state only moves forward, so re-running March from a pipeline
    that has already loaded April would read April. Brevis's window comes from
    the CRON, so the March run produces March's window -- and dlt, given an
    `end_value`, does not touch its persisted state at all.
    """
    start = _at(engine_env["DLT_INTERVAL_START"])
    end = _at(engine_env["DLT_INTERVAL_END"])
    day = end - start

    # The same pipeline, re-run for the slot BEFORE the fixture's.
    monkeypatch.setenv("DLT_INTERVAL_START", (start - day).isoformat())
    monkeypatch.setenv("DLT_INTERVAL_END", start.isoformat())

    seen = {}
    loaded = [row["id"] for row in _resource(_rows(start, end), seen)]

    assert seen["initial_value"] == start - day
    assert seen["end_value"] == start
    # Only the row that belongs to the earlier slot -- a row the forward-only
    # cursor would never look at again.
    assert loaded == [1], f"the backfill read {loaded}, not the slot it was for"


def test_a_partial_window_is_refused_rather_than_half_applied(monkeypatch):
    """Half a window is not half a bridge.

    dlt resolves a partial interval to nothing and raises rather than falling
    back to its state. The engine sets both or neither -- asserted on the Go
    side -- and this is what would happen if that ever slipped.
    """
    monkeypatch.setenv("DLT_INTERVAL_START", "2026-03-10T04:00:00Z")
    monkeypatch.delenv("DLT_INTERVAL_END", raising=False)

    seen = {}
    rows = _rows(_at("2026-03-10T04:00:00Z"), _at("2026-03-11T04:00:00Z"))
    with pytest.raises(Exception) as caught:
        list(_resource(rows, seen))
    assert "ExternalSchedulerNotAvailable" in str(type(caught.value)) or (
        "scheduler" in str(caught.value).lower()
    ), f"a partial window raised something unrelated: {caught.value!r}"
