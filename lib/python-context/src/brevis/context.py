"""Read what the steps before published; publish something for the ones after.

# Why this is a module and not a class

``BrevisContext.get(...)`` as a classmethod suggests there could be two of them.
There cannot: a process is inside exactly one step of one run. The import says
that, and there is no object to pass around or to mock.

# How it reaches the engine

It does not. There is no API call, no token, no port and no network.

The engine hands a step ``BREVIS_INPUT`` when it starts it, and reads back the
file at ``BREVIS_OUTPUT`` when it ends -- under Kubernetes that path is
``/dev/termination-log``, which the engine already reads to get the exit code.
So this package is a JSON parse and a file write, which is why it is pure
standard library and why its tests need no cluster, no database and no engine.

# The ceiling

4096 bytes, and it is the platform's number rather than ours. The termination
message is TRUNCATED rather than refused, so an oversized object arrives cut
mid-string and reads downstream as "this step published nothing". Refusing
before writing is what turns that into a message.
"""

from __future__ import annotations

import atexit
import json
import logging
import os
import sys
import tempfile
from typing import Any, Mapping

__all__ = [
    "ContextError",
    "NotVisible",
    "TooLarge",
    "get",
    "of",
    "published",
    "set",
]

ENV_INPUT = "BREVIS_INPUT"
ENV_OUTPUT = "BREVIS_OUTPUT"
ENV_RUN_ID = "BREVIS_RUN_ID"

#: The prefix the engine looks for on stdout. The phases use it, the metrics use
#: it, and the context uses it when there is no file to write.
#:
#: Defined HERE and imported by :mod:`brevis.metrics` rather than written twice.
#: A protocol literal in two files is a protocol that changes in one of them.
MARKER = "@brevis:"

#: Kubernetes truncates the termination message at this size. See the module
#: docstring: inheriting the platform's ceiling is the design.
MAX_BYTES = 4096

_log = logging.getLogger("brevis")


class ContextError(Exception):
    """Anything this module refuses."""


class NotVisible(ContextError, KeyError):
    """A step was read that this one does not depend on.

    It subclasses KeyError so ``except KeyError`` around a lookup keeps working,
    and ContextError so a caller can tell it from a plain missing dict key.
    """


class TooLarge(ContextError):
    """More was published than the termination message can carry."""


# --------------------------------------------------------------------------
# Reading
# --------------------------------------------------------------------------


def _incoming() -> dict[str, dict[str, Any]]:
    raw = os.environ.get(ENV_INPUT, "").strip()
    if not raw:
        return {}
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError as exc:  # pragma: no cover - the engine writes it
        raise ContextError(
            f"{ENV_INPUT} is not valid JSON. The engine writes it, so this means a "
            f"version mismatch rather than anything you did: {exc}"
        ) from exc
    if not isinstance(parsed, dict):
        raise ContextError(f"{ENV_INPUT} has to be a JSON object, and it is {type(parsed).__name__}")
    return parsed


def get(key: str, default: Any = None) -> Any:
    """Read one value published by a step this one depends on.

        bucket = context.get("extract.bucket")

    The key is ALWAYS qualified by the step that wrote it, and a bare
    ``get("bucket")`` is refused naming the steps that published it.

    That is not ceremony. Steps are isolated by design -- ``extract`` and
    ``transform`` cannot overwrite each other, because each writes only its own
    namespace -- and a bare key throws that away the moment two of them publish
    ``bucket``. Searching and picking one is precedence by accident: it works
    until it silently does not, and somebody spends an afternoon on a value that
    came from the wrong step.

    An absent key is not an error; that is what ``default`` is for. An absent
    STEP is, because it means the pipeline does not say what the code assumes.
    """
    step, sep, field = key.partition(".")
    if not sep or not field:
        raise ContextError(_unqualified(key))

    incoming = _incoming()
    if step not in incoming:
        raise NotVisible(_not_visible(step, sorted(incoming)))
    return incoming[step].get(field, default)


def of(step: str) -> Mapping[str, Any]:
    """Everything one step published, as a read-only mapping.

    For the case where a step publishes several related values and the reader
    wants them together. It returns a copy: mutating what came in would look
    like it changed something.
    """
    incoming = _incoming()
    if step not in incoming:
        raise NotVisible(_not_visible(step, sorted(incoming)))
    return dict(incoming[step])


def _unqualified(key: str) -> str:
    owners = [s for s, values in _incoming().items() if key in values]
    if owners:
        suggestion = ", ".join(f'"{s}.{key}"' for s in sorted(owners))
        return (
            f'get("{key}") does not say which step published it, and '
            f"{len(owners)} did. Ask for one of: {suggestion}"
        )
    return (
        f'get("{key}") does not say which step published it. The form is '
        f'get("<step>.{key}") -- context is keyed by the step that wrote it, so '
        f"two steps can publish the same name without either losing it"
    )


def _not_visible(step: str, visible: list[str]) -> str:
    if not visible:
        return (
            f'no context is visible to this step, so "{step}" cannot be read. '
            f"Add it to this step's depends_on in the workflow"
        )
    return (
        f'step "{step}" is not visible to this step. Visible: '
        f"{', '.join(visible)}. Add \"{step}\" to depends_on"
    )


# --------------------------------------------------------------------------
# Writing
# --------------------------------------------------------------------------

_pending: dict[str, Any] = {}
_written = False
_warned = False


def set(*mapping: Mapping[str, Any], **values: Any) -> None:  # noqa: A001
    """Publish values for the steps that depend on this one.

        context.set(name="Daniel")
        context.set(label="Nome")
        context.set({"partitions": ["2026-09-06", "2026-09-07"]})

    Calls MERGE. Those two lines leave both ``name`` and ``label`` published;
    setting the same key twice keeps the last write. Replacing wholesale would
    let a helper that publishes one key silently erase what the caller
    published, which is the kind of bug that only shows up in the step after.

    There is no step argument, and that is what makes the isolation structural:
    the only namespace a process can write is its own.

    Nothing reaches the disk until the process exits. A step that dies hard
    publishes nothing -- which is correct, because a crashed step's output
    described work that did not finish.
    """
    if _written:
        raise ContextError(
            "context.set() was called after the context was already written. That "
            "happens when set() runs inside an atexit handler registered before "
            "this module was imported; move the call into your program's normal flow"
        )

    merged: dict[str, Any] = {}
    for m in mapping:
        if not isinstance(m, Mapping):
            raise ContextError(
                f"context.set() takes keyword arguments or a mapping, and got "
                f"{type(m).__name__}"
            )
        merged.update(m)
    merged.update(values)

    for key, value in merged.items():
        if not isinstance(key, str):
            raise ContextError(
                f"context key {key!r} is a {type(key).__name__}, and JSON object keys "
                f"are strings. It would come back as \"{key}\" and the lookup would miss"
            )
        _check_serializable(key, value)

    if not _enabled():
        return

    _pending.update(merged)
    _check_size()


def published() -> Mapping[str, Any]:
    """What this step has published so far, for a test or an assertion."""
    return dict(_pending)


def _check_serializable(key: str, value: Any) -> None:
    try:
        json.dumps(value)
    except (TypeError, ValueError) as exc:
        hint = ""
        if hasattr(value, "isoformat"):
            hint = ". For a date or a datetime, pass .isoformat()"
        raise ContextError(
            f"context key {key!r} holds a {type(value).__name__}, which is not JSON: "
            f"{exc}{hint}"
        ) from exc


def _check_size() -> None:
    encoded = json.dumps(_pending, separators=(",", ":"), sort_keys=True)
    size = len(encoded.encode("utf-8"))
    if size <= MAX_BYTES:
        return

    # Naming the largest keys is the useful half: "4.6 KB is too big" leaves the
    # caller to find which value it was.
    largest = sorted(
        ((k, len(json.dumps(v, separators=(",", ":")).encode("utf-8"))) for k, v in _pending.items()),
        key=lambda kv: -kv[1],
    )[:3]
    culprits = ", ".join(f"{k} ({n} bytes)" for k, n in largest)
    raise TooLarge(
        f"the context is {size} bytes and the ceiling is {MAX_BYTES}. It travels in the "
        f"container's termination message, which the platform TRUNCATES rather than "
        f"refuses -- so an oversized object arrives cut in half and reads downstream as "
        f"'this step published nothing'. Largest: {culprits}. Publish a reference (a "
        f"path, a watermark, a count) and leave the data where it is"
    )


# --------------------------------------------------------------------------
# Running outside Brevis
# --------------------------------------------------------------------------


def _enabled() -> bool:
    """Whether anything published will actually be read.

    Said once, at INFO, and not raised. A script that cannot be run by hand
    cannot be developed -- and a set() that quietly did nothing in production is
    exactly the silent failure this library is against, so the line is what
    tells the two apart.
    """
    global _warned
    if os.environ.get(ENV_OUTPUT):
        return True
    # No output path but a run id means an executor with no return path of its
    # own -- a host the engine did not create. What is published then goes out
    # as a marked line on stdout, which every executor that streams logs has.
    if os.environ.get(ENV_RUN_ID):
        return True
    if not _warned:
        _warned = True
        _log.info(
            "brevis: not running under Brevis (neither %s nor %s is set), so "
            "context.set() is a no-op. Under the engine it publishes to the steps "
            "that depend on this one",
            ENV_OUTPUT,
            ENV_RUN_ID,
        )
    return False


def _flush() -> None:
    """Write once, at exit. Registered on import, so a caller need not."""
    global _written
    if _written or not _pending:
        return
    path = os.environ.get(ENV_OUTPUT)
    if not path:
        # No file: publish on stdout instead. There is deliberately no second
        # check for the run id here -- ``_enabled()`` is the one gate, and
        # nothing reaches ``_pending`` outside the engine, so a guard here would
        # be a branch no test could ever reach. A mutation proved exactly that:
        # removing it changed nothing anywhere.
        _written = True
        _announce(json.dumps(_pending, separators=(",", ":"), sort_keys=True))
        return

    _written = True
    payload = json.dumps(_pending, separators=(",", ":"), sort_keys=True)

    try:
        # /dev/termination-log is a character device: it is written directly,
        # never renamed into place. A temp-file-and-rename would fail there, and
        # atomicity is not needed because the engine reads this exactly once,
        # after the process is gone.
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(payload)
    except OSError as exc:
        # Loud, and on stderr, because the step is about to be reported as
        # succeeded while the step after it will not find what it needs.
        _log.error(
            "brevis: could not publish the context to %s: %s. The steps depending on "
            "this one will not see it",
            path,
            exc,
        )


def _announce(payload: str) -> None:
    """Publish on stdout, for an executor with no return path of its own.

    ONE write, flushed.

    One write rather than ``print()``, because ``print()`` does two -- the text,
    then the newline -- and stdout is shared with everything else the step logs.
    A second thread writing in between splits the marker across two lines, and
    the engine's scanner breaks on lines, so what it sees is not a marker at all.

    Flushed because a step killed by a timeout or a cancel after publishing
    would otherwise lose the line entirely.

    The file is preferred wherever there is one. Two roads carrying the same
    thing is what drifts, and a file is the road the platform vouches for.
    """
    sys.stdout.write(f'{MARKER}{{"type":"context","value":{payload}}}\n')
    sys.stdout.flush()


atexit.register(_flush)


def _reset_for_tests(output: str | None = None) -> str:
    """Clear the module's state between tests, and point the output somewhere.

    Exported with a leading underscore because it is not part of the contract:
    a real step is one process, one step, one write, and has no reason to reset.
    """
    global _written, _warned
    _pending.clear()
    _written = False
    _warned = False
    if output is None:
        output = os.path.join(tempfile.mkdtemp(), "output")
    os.environ[ENV_OUTPUT] = output
    return output
