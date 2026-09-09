"""What a step reports, and what it is refused for.

The whole contract is one line on stdout, so a test captures stdout.
"""

import json

import pytest

from brevis import metrics
from brevis.metrics import MetricError


def emitted(capsys):
    """The markers written so far, parsed."""
    out = capsys.readouterr().out
    return [json.loads(l[len(metrics.MARKER):])
            for l in out.splitlines() if l.startswith(metrics.MARKER)]


def test_a_gauge_and_a_counter_carry_what_the_engine_reads(capsys):
    """The fields are a CONTRACT with internal/application/execution/stages.go,
    and the two live in different languages -- nothing but a test naming them
    would notice a drift."""
    metrics.set("rows_loaded", 48213)
    metrics.inc("vendor_rejected_total")
    metrics.inc("bytes_discarded_total", 4096)

    got = emitted(capsys)
    assert [m["type"] for m in got] == ["metric"] * 3
    assert got[0]["name"] == "rows_loaded"
    assert got[0]["kind"] == "gauge"
    assert got[0]["value"] == 48213
    assert got[1]["kind"] == "counter"
    assert got[1]["value"] == 1          # inc defaults to one
    assert got[2]["value"] == 4096


def test_an_invalid_name_is_refused_and_never_written(capsys):
    """A name Prometheus cannot parse does not lose one series -- it makes the
    WHOLE scrape fail, every other metric with it. So it is refused here, where
    the mistake was made, rather than renamed into something nobody wrote."""
    for bad in ["rows-loaded", "rows loaded", "2rows", "", "métrica", "a.b"]:
        with pytest.raises(MetricError) as e:
            metrics.set(bad, 1)
        # The message says the RULE, not just that it is wrong.
        assert "rows_loaded" in str(e.value)

    assert emitted(capsys) == []


def test_a_value_that_is_not_a_number_is_refused(capsys):
    """`metrics.set("ok", True)` reporting a 1 is a number whose meaning depends
    on knowing that bool is an int in Python."""
    for bad in [True, False, "12", None, [1]]:
        with pytest.raises(MetricError):
            metrics.set("x", bad)
    assert emitted(capsys) == []


def test_a_counter_cannot_go_down(capsys):
    """Every rate() over a counter assumes it only rises, and a counter that
    falls breaks the graph weeks later rather than at the call."""
    with pytest.raises(MetricError) as e:
        metrics.inc("x_total", -1)
    assert "only goes up" in str(e.value)
    assert emitted(capsys) == []

    # And set() is what a value moving both ways is for.
    metrics.set("queue_depth", -1)
    assert emitted(capsys)[0]["value"] == -1


def test_each_metric_is_one_line(capsys):
    """The engine reads this pipe line by line. A metric split over two lines is
    two log lines it cannot parse, and half a marker reaches the log."""
    metrics.set("a", 1)
    metrics.set("b", 2)
    out = capsys.readouterr().out
    lines = [l for l in out.splitlines() if l.strip()]
    assert len(lines) == 2
    assert all(l.startswith(metrics.MARKER) for l in lines)


def test_the_module_has_no_dependencies():
    """The library depends on nothing and will not start now. This mirrors the
    allowlist gate in .github/scripts/python-check.sh, close to the code it
    guards."""
    import ast
    import pathlib

    src = pathlib.Path(metrics.__file__).read_text()
    mods = set()
    for node in ast.walk(ast.parse(src)):
        if isinstance(node, ast.Import):
            mods.update(a.name.split(".")[0] for a in node.names)
        elif isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
            mods.add(node.module.split(".")[0])
    assert mods <= {"__future__", "json", "re", "sys", "time", "typing"}
