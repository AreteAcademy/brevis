"""The library's whole contract, exercised with no cluster and no engine.

That property is not luck: the contract is two environment variables and a file
path, so a test sets them. It is the reason the contract is two environment
variables.
"""

import io
import json
import os
import pathlib
import subprocess
import sys
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


SRC = pathlib.Path(__file__).resolve().parent.parent / "src"


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


# The second road: an executor with no return path of its own.
#
# A pod writes to /dev/termination-log and the kubelet carries it back. A host
# the engine did not create has no equivalent, and the alternatives were to
# narrow the contract for those targets or to build a token-authenticated API.
# The pipe that already carries the phases carries this instead.
def test_with_no_output_path_it_publishes_on_stdout(monkeypatch, capsys):
    context._reset_for_tests()
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)
    monkeypatch.setenv(context.ENV_RUN_ID, "1f0a5b6c-0000-0000-0000-000000000001")

    context.set(watermark="2026-03-11T04:00:00Z", rows=48213)
    context._flush()

    out = capsys.readouterr().out.strip()
    assert out.startswith(context.MARKER), out
    event = json.loads(out[len(context.MARKER):])
    assert event["type"] == "context"
    assert event["value"] == {"rows": 48213, "watermark": "2026-03-11T04:00:00Z"}


# One line. The executor's scanner breaks on lines, so a marker split across two
# writes is a marker the engine never sees.
def test_the_marker_is_one_line(monkeypatch, capsys):
    context._reset_for_tests()
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)
    monkeypatch.setenv(context.ENV_RUN_ID, "run-1")

    context.set(a="1", b="2", c="3")
    context._flush()

    assert len(capsys.readouterr().out.strip().splitlines()) == 1


# A file beats stdout. Two roads carrying the same thing is what drifts, and the
# file is the one the platform vouches for.
def test_an_output_path_means_nothing_is_printed(monkeypatch, capsys, tmp_path):
    out = tmp_path / "output"
    context._reset_for_tests(str(out))
    monkeypatch.setenv(context.ENV_RUN_ID, "run-1")

    context.set(rows=7)
    context._flush()

    assert capsys.readouterr().out == ""
    assert json.loads(out.read_text()) == {"rows": 7}


# Run by hand, nothing is printed and nothing is written. A fetcher somebody is
# debugging must not have protocol appear in its terminal.
def test_outside_the_engine_nothing_is_printed(monkeypatch, capsys):
    context._reset_for_tests()
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)
    monkeypatch.delenv(context.ENV_RUN_ID, raising=False)

    context.set(rows=7)
    context._flush()

    assert capsys.readouterr().out == ""
    assert context.published() == {}


# The marker is FLUSHED, and this is the only way to prove it.
#
# capsys sees a write whether or not it was flushed, so the assertion has to be
# a real process that dies without running its exit handlers: os._exit skips
# both atexit and the interpreter's final flush. Unflushed, the line is simply
# lost -- which is the case this matters in, a step killed by a timeout or a
# cancel after it published.
def test_the_marker_survives_a_process_that_dies_hard(tmp_path):
    script = tmp_path / "step.py"
    script.write_text(
        "import os, sys\n"
        f"sys.path.insert(0, {str(SRC)!r})\n"
        "from brevis import context\n"
        "context.set(watermark='2026-03-11T04:00:00Z')\n"
        "context._flush()\n"
        "os._exit(0)\n"
    )
    env = {**os.environ, "BREVIS_RUN_ID": "run-1", "PATH": os.environ.get("PATH", "")}
    env.pop("BREVIS_OUTPUT", None)
    done = subprocess.run(
        [sys.executable, str(script)], capture_output=True, text=True, env=env, check=False
    )
    assert context.MARKER in done.stdout, (
        f"the marker did not survive os._exit; stdout={done.stdout!r} "
        f"stderr={done.stderr!r}"
    )
    event = json.loads(done.stdout.strip()[len(context.MARKER):])
    assert event["value"] == {"watermark": "2026-03-11T04:00:00Z"}


class _CountingStdout(io.TextIOBase):
    """Counts write() calls, so "one write" can be asserted rather than hoped."""

    def __init__(self) -> None:
        self.writes = 0
        self.text = ""

    def write(self, s: str) -> int:  # type: ignore[override]
        self.writes += 1
        self.text += s
        return len(s)


# ONE write, not one line.
#
# The difference is the whole point: print() does two writes -- the text, then
# the newline -- and stdout is shared with everything else the step logs. A
# second thread writing between them splits the marker across two lines, and the
# engine's scanner breaks on lines, so what arrives is not a marker at all.
#
# Asserted for BOTH halves of the protocol this library speaks, because they had
# drifted: the metric used print() and the context did not.
def test_the_protocol_takes_exactly_one_write(monkeypatch):
    from brevis import metrics

    context._reset_for_tests()
    monkeypatch.delenv(context.ENV_OUTPUT, raising=False)
    monkeypatch.setenv(context.ENV_RUN_ID, "run-1")

    for emit in (
        lambda: (context.set(a=1), context._flush()),
        lambda: metrics.set("rows_loaded", 1),
    ):
        counter = _CountingStdout()
        monkeypatch.setattr(sys, "stdout", counter)
        emit()
        monkeypatch.undo()
        monkeypatch.setenv(context.ENV_RUN_ID, "run-1")
        assert counter.writes == 1, (
            f"the marker took {counter.writes} writes: {counter.text!r}"
        )
        assert counter.text.endswith("\n")
        context._reset_for_tests()
        monkeypatch.delenv(context.ENV_OUTPUT, raising=False)
