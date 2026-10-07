"""What a step says it wrote, and what it is refused for.

The contract is one line on stdout that the engine reads -- the same line the
Go SDK writes after a load -- so a test captures stdout.
"""

import json
import pathlib

import pytest

import brevis
from brevis import landed
from brevis.context import MARKER
from brevis.landings import LandingError, check_target

HERE = pathlib.Path(__file__).parent


def emitted(capsys):
    out = capsys.readouterr().out
    return [json.loads(l[len(MARKER):])
            for l in out.splitlines() if l.startswith(MARKER)]


def fixture_cases():
    cases = []
    for n, raw in enumerate((HERE / "targets.txt").read_text().splitlines(), 1):
        if not raw.strip() or raw.startswith("#"):
            continue
        body, _, reason = raw.partition(" # ")
        verdict, _, target = body.partition(" ")
        cases.append((n, verdict, target.strip(), reason))
    assert cases, "the fixture has no cases"
    return cases


def test_the_fixture_is_an_exact_copy_of_the_sdks():
    """Three checkers -- the SDK, the engine, this library -- agree only by
    reading the same cases. A copy that drifts is a landing this library
    accepts and the engine drops, in silence."""
    sdk = HERE.parents[2] / "sdk" / "testdata" / "targets.txt"
    assert (HERE / "targets.txt").read_bytes() == sdk.read_bytes(), (
        "tests/targets.txt differs from sdk/testdata/targets.txt; copy the SDK's over it")


@pytest.mark.parametrize("line,verdict,target,reason", fixture_cases())
def test_every_fixture_target_is_judged_as_the_fixture_says(line, verdict, target, reason):
    if verdict == "valid":
        check_target(target)
    else:
        # A step lands on a table, never on a pattern -- a pattern is a
        # gateway's published auto_table, which the engine checks.
        with pytest.raises(LandingError):
            check_target(target)


def test_a_landing_is_one_line_the_engine_reads(capsys):
    landed("bigquery://acme-prod/silver/orders", rows=48213, bytes=9120334)
    got = emitted(capsys)
    assert len(got) == 1
    l = got[0]
    assert l["type"] == "landed"
    assert l["target"] == "bigquery://acme-prod/silver/orders"
    assert l["rows"] == 48213
    assert l["bytes"] == 9120334
    assert l["at"].endswith("Z")


def test_absent_counts_are_left_out_and_zero_is_kept(capsys):
    """Absent is not zero. A step that does not count says nothing, and the
    engine keeps that as unknown; a step that wrote nothing says 0."""
    landed("s3://acme-landing/vendors/")
    landed("postgres://analytics/public/orders", rows=0)
    a, b = emitted(capsys)
    assert "rows" not in a and "bytes" not in a
    assert b["rows"] == 0


def test_a_bad_target_is_refused_and_never_written(capsys):
    for bad in [
        "postgres://loader:secret@db.internal:5432/analytics/public/orders",
        "bigquery://acme-prod/bronze/*",
        "snowflake://acct/db/schema/t",
        "",
    ]:
        with pytest.raises(LandingError) as e:
            landed(bad)
        # The message names what a target looks like, not only that it is wrong.
        assert "bigquery://" in str(e.value)
    assert emitted(capsys) == []


def test_a_bad_count_is_refused(capsys):
    for kwargs in [{"rows": -1}, {"rows": 1.5}, {"rows": True}, {"bytes": "12"}]:
        with pytest.raises(LandingError):
            landed("postgres://analytics/public/orders", **kwargs)
    assert emitted(capsys) == []


def test_it_is_exported_from_the_package():
    assert brevis.landed is landed
    assert "landed" in brevis.__all__
