"""The library's whole contract, exercised with no cluster and no engine.

That property is not luck: the contract is two environment variables and a file
path, so a test sets them. It is the reason the contract is two environment
variables.
"""

import json
import logging

import pytest

from brevis import context
from brevis.context import ContextError, NotVisible, TooLarge

PIPELINE = json.dumps(
    {
        "extract": {"bucket": "s3://landing/2026-09-07", "rows": 48213},
        "transform": {"bucket": "s3://silver/2026-09-07", "rows": 47998},
    }
)


@pytest.fixture(autouse=True)
def clean(monkeypatch, tmp_path):
    monkeypatch.setenv(context.ENV_INPUT, PIPELINE)
    context._reset_for_tests(str(tmp_path / "output"))
    yield


# ---------------------------------------------------------------------------
# Reading
# ---------------------------------------------------------------------------


def test_it_reads_what_a_step_it_depends_on_published():
    assert context.get("extract.bucket") == "s3://landing/2026-09-07"
    assert context.get("transform.rows") == 47998


def test_two_steps_publishing_the_same_key_both_survive():
    """The isolation property, from the reader's side.

    `extract` and `transform` both published `bucket`, and neither lost. That is
    what keying by step buys, and it is why the qualified form is not ceremony.
    """
    assert context.get("extract.bucket") != context.get("transform.bucket")


def test_a_bare_key_is_refused_naming_the_steps_that_published_it():
    """Searching and picking one would be precedence by accident."""
    with pytest.raises(ContextError) as err:
        context.get("bucket")

    message = str(err.value)
    assert "extract.bucket" in message and "transform.bucket" in message, message


def test_a_bare_key_nobody_published_still_teaches_the_form():
    with pytest.raises(ContextError) as err:
        context.get("nope")
    assert 'get("<step>.nope")' in str(err.value)


def test_an_absent_key_is_not_an_error():
    assert context.get("extract.nope") is None
    assert context.get("extract.nope", default=0) == 0


def test_a_step_it_does_not_depend_on_points_at_depends_on():
    """The two causes need different fixes, so they get different messages."""
    with pytest.raises(NotVisible) as err:
        context.get("load.anything")

    message = str(err.value)
    assert "depends_on" in message, message
    assert "extract, transform" in message, message


def test_not_visible_is_still_a_keyerror():
    """So `except KeyError` around a lookup keeps working."""
    with pytest.raises(KeyError):
        context.get("load.anything")


def test_of_returns_a_copy():
    got = context.of("extract")
    got["bucket"] = "mutated"
    assert context.get("extract.bucket") == "s3://landing/2026-09-07"


# ---------------------------------------------------------------------------
# Writing
# ---------------------------------------------------------------------------


def test_two_calls_merge_rather_than_replace():
    """The question asked directly: set(name=...) then set(label=...).

    Both survive. Replacing wholesale would let a helper publishing one key
    silently erase what main() published -- a bug that only shows up in the step
    after this one.
    """
    context.set(name="Daniel")
    context.set(label="Nome")

    assert context.published() == {"name": "Daniel", "label": "Nome"}


def test_the_same_key_twice_keeps_the_last_write():
    context.set(rows=1)
    context.set(rows=2)
    assert context.published()["rows"] == 2


def test_a_mapping_works_as_well_as_keywords():
    context.set({"partitions": ["2026-09-06", "2026-09-07"]})
    context.set(rows=2)
    assert context.published() == {"partitions": ["2026-09-06", "2026-09-07"], "rows": 2}


def test_it_writes_once_at_exit(tmp_path):
    out = tmp_path / "written"
    context._reset_for_tests(str(out))

    context.set(name="Daniel")
    context.set(label="Nome")
    assert not out.exists(), "nothing should reach the disk before the process ends"

    context._flush()
    assert json.loads(out.read_text()) == {"label": "Nome", "name": "Daniel"}


def test_writing_twice_is_refused():
    context.set(rows=1)
    context._flush()
    with pytest.raises(ContextError):
        context.set(rows=2)


# ---------------------------------------------------------------------------
# What it refuses, and each refusal is a silent failure turned into a message
# ---------------------------------------------------------------------------


def test_a_value_that_is_not_json_is_refused_naming_the_key():
    class Opaque:
        pass

    with pytest.raises(ContextError) as err:
        context.set(thing=Opaque())
    assert "thing" in str(err.value)


def test_a_datetime_is_refused_with_the_fix_in_the_message():
    import datetime

    with pytest.raises(ContextError) as err:
        context.set(when=datetime.datetime(2026, 9, 7))
    assert "isoformat" in str(err.value)


def test_a_non_string_key_is_refused():
    with pytest.raises(ContextError):
        context.set({1: "x"})


def test_going_over_the_ceiling_is_refused_before_writing(tmp_path):
    """The refusal that matters most.

    The platform TRUNCATES the termination message rather than refusing it, so
    an oversized object arrives cut mid-string and reads downstream as "this
    step published nothing". Refusing here is what turns that into a message.
    """
    out = tmp_path / "never"
    context._reset_for_tests(str(out))

    with pytest.raises(TooLarge) as err:
        context.set(rows=["x" * 100 for _ in range(60)])

    message = str(err.value)
    assert "rows" in message, "the message does not name the culprit: " + message
    assert str(context.MAX_BYTES) in message
    assert not out.exists()


def test_the_ceiling_counts_everything_published_not_one_call(tmp_path):
    """Two calls under the limit that together exceed it are still refused."""
    context._reset_for_tests(str(tmp_path / "never"))
    context.set(a="x" * 2000)
    with pytest.raises(TooLarge):
        context.set(b="y" * 3000)


# ---------------------------------------------------------------------------
# Outside Brevis
# ---------------------------------------------------------------------------


def test_running_by_hand_is_a_no_op_that_says_so(monkeypatch, caplog):
    """A script that cannot be run by hand cannot be developed.

    So it does not raise. But a set() that quietly did nothing in production is
    the silent failure this library is against, and the line is what tells the
    two apart.
    """
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)
    monkeypatch.delenv(context.ENV_INPUT, raising=False)
    context._reset_for_tests("")
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)

    with caplog.at_level(logging.INFO, logger="brevis"):
        context.set(name="Daniel")

    assert context.published() == {}
    assert any("not running under Brevis" in r.message for r in caplog.records)


def test_running_by_hand_still_validates(monkeypatch):
    """The guards are not skipped outside the engine.

    Otherwise a value that works on a laptop fails in production, which is the
    worst place to learn it.
    """
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)
    context._reset_for_tests("")
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)

    with pytest.raises(ContextError):
        context.set(thing=object())


def test_no_input_means_every_step_is_invisible(monkeypatch):
    monkeypatch.delenv(context.ENV_INPUT, raising=False)
    with pytest.raises(NotVisible) as err:
        context.get("extract.bucket")
    assert "depends_on" in str(err.value)


# ---------------------------------------------------------------------------
# The wire format, which the engine and the Go SDK share
# ---------------------------------------------------------------------------


def test_what_it_writes_is_what_the_engine_parses(tmp_path):
    """One object, string keys, no wrapper.

    Pinned because three implementations read it: this library, the Go SDK, and
    a bash step with jq. A wrapper added here would be invisible until one of
    the other two failed to find a key.
    """
    out = tmp_path / "wire"
    context._reset_for_tests(str(out))
    context.set(bucket="s3://x", rows=48213)
    context._flush()

    raw = out.read_text()
    assert raw == '{"bucket":"s3://x","rows":48213}'
    assert json.loads(raw) == {"bucket": "s3://x", "rows": 48213}
