"""Numbers only your pipeline knows, on the engine's own /metrics.

    from brevis import metrics

    metrics.set("rows_loaded", 48213)
    metrics.inc("vendor_rejected_total")

# Why this does not open a port

Because a step is not scrapeable. `:9090` belongs to the scheduler and the API
-- long-lived processes with a stable address, scraped every fifteen to sixty
seconds. A step starts in its own pod, runs for forty seconds and exits: a port
it opened would be scraped never, or once by luck.

The three known answers to that are a Pushgateway (a component to operate, and
series that stay until somebody deletes them), an OTLP collector (a component
AND a dependency in this library, which has none and will not get one), or
letting the ORCHESTRATOR carry them. Brevis already reads every step's stdout
looking for `@brevis:` lines -- that is how the SDK's phases reach the graph --
and it already has a meter and a Prometheus endpoint.

So this writes one line to stdout and the engine does the rest. The metric
arrives labelled with the workflow and the step for free, because the engine
knows what it was running; through a Pushgateway this library would have to
label itself, and would get it wrong.

# Outside the engine

The line still goes to stdout, where it reads as what it is. Nothing collects
it and nothing fails -- the same as `context.set()` on a laptop.
"""

from __future__ import annotations

import json
import re
import sys
import time
from typing import Union

__all__ = ["MetricError", "inc", "set"]

#: The prefix the engine looks for. The same one the phases and the published
#: context use -- imported rather than repeated, because a protocol literal in
#: two files is a protocol that changes in one of them.
from .context import MARKER  # noqa: E402

# Prometheus's own rule for a metric name.
#
# A name that breaks it does not produce a broken metric -- it produces an
# exposition Prometheus REFUSES TO PARSE, and the whole scrape is lost, every
# other metric with it. So it is refused HERE, where the mistake was made,
# rather than normalised into a name nobody wrote.
_NAME = re.compile(r"^[a-zA-Z_][a-zA-Z0-9_]*$")

Number = Union[int, float]


class MetricError(ValueError):
    """A metric this module refuses to report."""


def _emit(name: str, value: Number, kind: str) -> None:
    if not isinstance(name, str) or not _NAME.match(name):
        raise MetricError(
            f"{name!r} is not a valid metric name. Prometheus accepts letters, "
            f"digits and underscore, and the first character cannot be a digit "
            f"-- so `rows_loaded` and not `rows-loaded` or `rows loaded`. "
            f"A name it refuses costs the WHOLE scrape, not just this metric, "
            f"which is why it is refused here instead of being renamed for you."
        )
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        # bool is an int in Python, and `metrics.set("ok", True)` reporting a 1
        # is a number whose meaning depends on knowing that.
        raise MetricError(
            f"the value of {name!r} is a {type(value).__name__}; a metric is a "
            f"number. Report the count or the duration, not the state."
        )

    line = MARKER + json.dumps(
        {
            "type": "metric",
            "name": name,
            "kind": kind,
            "value": value,
            "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        },
        separators=(",", ":"),
        sort_keys=True,
    )
    # ONE write, and flushed.
    #
    # Flushed because the engine reads this pipe LIVE: a buffered line arrives
    # when the process exits, which for a step that runs for an hour is an hour
    # late and for a step that is killed is never.
    #
    # One write rather than print(), because print() does two -- the text, then
    # the newline -- and stdout is shared with everything else the step logs. A
    # second thread writing in between splits the marker across two lines, and
    # the engine's scanner breaks on lines, so what it sees is not a marker at
    # all. Cheap to avoid, and it makes this identical to the Go SDK's.
    sys.stdout.write(line + "\n")
    sys.stdout.flush()


def set(name: str, value: Number) -> None:  # noqa: A001 - it is a gauge's verb
    """Report a gauge: the last value wins.

        metrics.set("rows_loaded", 48213)

    After the step exits the value stays until the next run writes another,
    which is what "how many rows did the last run load" means. For something
    that adds up across runs, use `inc`.
    """
    _emit(name, value, "gauge")


def inc(name: str, by: Number = 1) -> None:
    """Report a counter: the values add up, across steps and across runs.

        metrics.inc("vendor_rejected_total")
        metrics.inc("bytes_discarded_total", 4096)

    Counters only go up. A negative `by` is refused rather than quietly
    accepted: a counter that goes down breaks every rate() built on it, and the
    breakage shows up in a graph weeks later.
    """
    if not isinstance(by, bool) and isinstance(by, (int, float)) and by < 0:
        raise MetricError(
            f"inc({name!r}, {by}) would make a counter go down. A counter only "
            f"goes up -- every rate() over it assumes so. Use set() for a value "
            f"that moves in both directions."
        )
    _emit(name, by, "counter")
