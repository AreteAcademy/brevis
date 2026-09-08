"""What the engine injects, exercised with no engine.

The contract is one environment variable, so a test sets it. That is the same
property the context tests have, and for the same reason.
"""

import json
import logging
import os
import pathlib
from datetime import datetime, timedelta, timezone

import pytest

from brevis import run

# A scheduled run, thirty-seven minutes late, after one that failed.
LATE = json.dumps(
    {
        "scheduled_at": "2026-09-08T04:00:00Z",
        "started_at": "2026-09-08T04:37:00Z",
        "adjusted_at": "2026-09-08T04:00:00Z",
        "delay_seconds": 2220,
        "interval_start": "2026-09-07T04:00:00Z",
        "interval_end": "2026-09-08T04:00:00Z",
        "previous_error": True,
        "previous_success_at": "2026-09-06T04:00:00Z",
        "date": "2026-09-08",
    }
)

SLOT = datetime(2026, 9, 8, 4, 0, tzinfo=timezone.utc)


@pytest.fixture(autouse=True)
def clean(monkeypatch):
    """No test inherits another's environment, or the engine's."""
    for name in (
        run.ENV_AUTO_PARAMS,
        run.ENV_MAP_INDEX,
        run.ENV_MAP_VALUE,
        run.ENV_RUN_ID,
        run.ENV_RUN_FIRST,
        run.ENV_RUN_ATTEMPT,
        run.ENV_RUN_TRIGGER,
        run.ENV_RUN_LOGICAL_DATE,
        run.ENV_RUN_PARAMS,
    ):
        monkeypatch.delenv(name, raising=False)


def test_a_late_run_reads_the_slot_and_not_the_wall_clock(monkeypatch):
    """The whole reason this module exists.

    The run started at 04:37 and the clock says 04:00. Reading the start would
    make the next slot's window begin thirty-seven minutes late, and those
    thirty-seven minutes would belong to no run at all.
    """
    monkeypatch.setenv(run.ENV_AUTO_PARAMS, LATE)
    assert run.now() == SLOT
    assert run.auto().started_at == SLOT + timedelta(minutes=37)
    assert run.auto().delay_seconds == 2220


def test_the_window_is_the_previous_slot_to_this_one(monkeypatch):
    monkeypatch.setenv(run.ENV_AUTO_PARAMS, LATE)
    since, until = run.window()
    assert since == SLOT - timedelta(days=1)
    # The end is the slot itself, EXCLUDED: what arrives after it belongs to
    # the next run.
    assert until == SLOT


def test_a_z_suffix_parses_on_the_declared_minimum():
    """`datetime.fromisoformat` refused a `Z` until 3.11.

    The engine writes RFC 3339, which always uses one, and every field here is
    optional -- so on the 3.9 floor this library would have parsed nothing at
    all and said nothing about it. This test is the reason `_moment` translates
    the suffix instead of handing the string to the standard library.
    """
    assert run._moment("2026-09-08T04:00:00Z") == SLOT
    assert run._moment("2026-09-08T01:00:00-03:00") == SLOT
    # A naive timestamp is read as UTC rather than as the local zone: guessing
    # would make the same run read differently on a laptop and in a pod.
    assert run._moment("2026-09-08T04:00:00") == SLOT


def test_without_the_engine_now_is_the_wall_clock():
    """A script somebody runs by hand behaves as it did before this existed."""
    auto = run.auto()
    assert auto.from_engine is False
    assert auto.window() is None
    assert auto.date == ""
    assert abs((datetime.now(timezone.utc) - auto.now()).total_seconds()) < 60


def test_a_workflow_without_a_schedule_has_no_window(monkeypatch):
    """`None`, and not a pair of zero times.

    A query from the epoch selects everything, which is the failure this
    refuses to make silent.
    """
    monkeypatch.setenv(
        run.ENV_AUTO_PARAMS,
        json.dumps({"adjusted_at": "2026-09-08T04:00:00Z", "date": "2026-09-08"}),
    )
    assert run.window() is None
    assert run.now() == SLOT

    # And the caller's own fallback is one line, which is the point of `None`.
    since, until = run.window() or (run.now() - timedelta(days=1), run.now())
    assert (since, until) == (SLOT - timedelta(days=1), SLOT)


def test_a_malformed_value_loses_the_params_and_never_the_run(monkeypatch, caplog):
    """Whatever wrote that variable is broken; failing a load over it would
    turn a cosmetic bug into an incident."""
    monkeypatch.setenv(run.ENV_AUTO_PARAMS, "{not json")
    with caplog.at_level(logging.WARNING, logger="brevis"):
        auto = run.auto()
    assert auto.adjusted_at is None
    assert "malformed" in caplog.text
    # And `now()` still answers, so the step keeps working.
    assert auto.now().year >= 2026


def test_a_malformed_timestamp_drops_only_itself(monkeypatch, caplog):
    monkeypatch.setenv(
        run.ENV_AUTO_PARAMS,
        json.dumps(
            {
                "adjusted_at": "2026-09-08T04:00:00Z",
                "interval_start": "yesterday",
                "interval_end": "2026-09-08T04:00:00Z",
                "delay_seconds": "not a number",
                "date": "2026-09-08",
            }
        ),
    )
    with caplog.at_level(logging.WARNING, logger="brevis"):
        auto = run.auto()
    assert auto.now() == SLOT  # the value that matters survived
    assert auto.window() is None  # half a window is no window
    assert auto.delay_seconds == 0


def test_the_run_context_carries_everything(monkeypatch):
    monkeypatch.setenv(run.ENV_RUN_ID, "018f-abc")
    monkeypatch.setenv(run.ENV_RUN_FIRST, "true")
    monkeypatch.setenv(run.ENV_RUN_ATTEMPT, "2")
    monkeypatch.setenv(run.ENV_RUN_TRIGGER, "schedule")
    monkeypatch.setenv(run.ENV_RUN_LOGICAL_DATE, "2026-09-08T04:00:00Z")
    monkeypatch.setenv(run.ENV_RUN_PARAMS, '{"load_full": "true"}')
    monkeypatch.setenv(run.ENV_AUTO_PARAMS, LATE)

    rc = run.context()
    assert rc.from_engine is True
    assert (rc.id, rc.first, rc.attempt, rc.trigger) == ("018f-abc", True, 2, "schedule")
    assert rc.logical_date == SLOT
    assert rc.params == {"load_full": "true"}
    assert rc.auto.previous_error is True
    assert rc.auto.previous_success_at == SLOT - timedelta(days=2)


def test_params_are_strings_and_never_none(monkeypatch):
    assert run.params() == {}
    assert run.param("missing") == ""
    assert run.param("missing", default="0") == "0"

    # The engine writes strings; a number that slipped through comes back as
    # one, because `params["x"] == "1"` is what every step is written against.
    monkeypatch.setenv(run.ENV_RUN_PARAMS, '{"retries": 3}')
    assert run.params() == {"retries": "3"}


def test_the_environment_is_read_on_every_call(monkeypatch):
    """Not snapshotted at import.

    A module that reads the environment at import time behaves differently
    depending on import order, and no test can set a variable after it.
    """
    assert run.auto().date == ""
    monkeypatch.setenv(run.ENV_AUTO_PARAMS, LATE)
    assert run.auto().date == "2026-09-08"


def test_the_engine_writes_exactly_what_this_reads():
    """The fixture is written by the ENGINE's own test.

    These auto params cross a module boundary and then a language boundary:
    Go's `run.AutoParams.Env()` writes the JSON, this module parses it, and
    nothing compiles both. A field renamed on that side would break every
    Python step in the fleet with a green build at both ends.

    So the two share a file. `TestTheEngineAndThePythonLibraryAgreeOnTheContract`
    in internal/domain/run writes it; this reads the same bytes. Rename a JSON
    tag and one of the two goes red immediately.
    """
    fixture = pathlib.Path(__file__).with_name("engine_auto_params.json")
    os.environ["BREVIS_AUTO_PARAMS"] = fixture.read_text()
    try:
        auto = run.auto()
    finally:
        del os.environ["BREVIS_AUTO_PARAMS"]

    # Every field, by name. A loop over the dataclass would pass while reading
    # nothing, because every field has a default.
    assert auto.scheduled_at == SLOT
    assert auto.started_at == SLOT + timedelta(minutes=37)
    assert auto.adjusted_at == SLOT
    assert auto.now() == SLOT
    assert auto.delay_seconds == 2220
    assert auto.window() == (SLOT - timedelta(days=1), SLOT)
    assert auto.previous_error is True
    assert auto.previous_success_at == SLOT - timedelta(days=2)
    assert auto.date == "2026-09-08"
    assert auto.from_engine is True


# --------------------------------------------------------------------------
# for_each
# --------------------------------------------------------------------------


def test_a_mapped_step_knows_which_element_it_got(monkeypatch):
    """`for_each:` is one node on the graph and one process per element.

    Without this, every Python step under `for_each:` reads
    os.environ["BREVIS_MAP_VALUE"] by hand -- and the ones that forget the
    engine leaves the variable OUT on an unmapped step read "" and carry on.
    """
    monkeypatch.setenv(run.ENV_MAP_INDEX, "2")
    monkeypatch.setenv(run.ENV_MAP_VALUE, "2026-01")
    assert run.map_index() == 2
    # A JSON string arrives WITHOUT its quotes: `for_each` over ["2026-01"]
    # hands a step 2026-01, which is what the command was written expecting.
    assert run.map_value() == "2026-01"

    rc = run.context()
    assert (rc.map_index, rc.map_value) == (2, "2026-01")


def test_an_unmapped_step_has_no_index_and_no_element():
    """`None`, not `-1` and not `""`.

    A sentinel is a number somebody eventually does arithmetic on, and an empty
    string is indistinguishable from an element that IS the empty string. The
    engine leaves the variables out, so the two stay distinguishable here.
    """
    assert run.map_index() is None
    assert run.map_value() is None
    assert run.context().map_index is None


def test_a_mapped_step_over_objects_gets_json(monkeypatch):
    """Anything that is not a JSON string arrives as its JSON.

    That is the engine's rule, and the test is here so the docstring telling
    people to `json.loads` it cannot quietly become wrong.
    """
    monkeypatch.setenv(run.ENV_MAP_INDEX, "0")
    monkeypatch.setenv(run.ENV_MAP_VALUE, '{"sku": "A1", "rows": 3}')
    assert json.loads(run.map_value()) == {"sku": "A1", "rows": 3}


def test_the_element_can_be_the_empty_string(monkeypatch):
    """And that is not the same as not being mapped."""
    monkeypatch.setenv(run.ENV_MAP_INDEX, "1")
    monkeypatch.setenv(run.ENV_MAP_VALUE, "")
    assert run.map_value() == ""
    assert run.map_value() is not None
