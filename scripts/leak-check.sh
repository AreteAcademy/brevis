#!/usr/bin/env bash
# A test harness that is KILLED must not leave data in a customer's warehouse.
#
# `t.Cleanup` runs after a failure and after a panic the framework catches. It
# does NOT run after a SIGKILL, an OOM, a laptop that sleeps until the
# connection dies, or a `^C` the shell escalates. Every one of those happens,
# and what is left behind is a dataset beside a client's real data, in a
# project that is not ours to litter.
#
# So the harnesses ask BigQuery to throw the dataset's tables away after a day
# -- dialect.Disposable -- and this is what proves they did. Nothing in
# `go test` can: a test that kills its own process cannot then assert
# anything, which is why this is a script.
#
# NOT IN CI, for the reason the whole warehouse decision gives: the repository
# is public and this needs a real project. Run by hand, beside `make warehouse`.
#
# WHAT A GREEN RUN DOES NOT MEAN. BigQuery has no dataset expiry, only one for
# the tables inside. A killed harness leaks an EMPTY DATASET, forever. This
# proves it is empty-by-tomorrow, not that it is gone.
#
# EVERY QUESTION GOES THROUGH GO, not through `bq`. The first version shelled
# out and `bq` demanded an interactive re-auth it could not get -- while every
# test here was talking to the same project happily, over Application Default
# Credentials through the Go client. One credential path for everything that
# reaches a warehouse.
set -uo pipefail

cd "$(dirname "$0")/.."

fail() { echo "::error::$*" >&2; exit 1; }

PROJECT="${BREVIS_SQL_IT_BQ_PROJECT:-${BREVIS_IT_PROJECT:-}}"
[ -n "$PROJECT" ] || fail "set BREVIS_SQL_IT_BQ_PROJECT (or BREVIS_IT_PROJECT) to a project you are willing to lose"

probe() { ( cd sql && go run ./internal/dialect/dialecttest/leakprobe "$@" ); }

# THE NAME IS GENERATED HERE AND NOWHERE ELSE, and `leakprobe drop` refuses
# anything that is not this shape. A sweep by pattern is one unquoted variable
# from a customer's dataset.
LEAK="bvs_kill_$(date +%s)_$$"
echo "project: $PROJECT"
echo "leak:    $LEAK"
echo

# --- a harness, about to die ----------------------------------------------
probe make "$PROJECT" "$LEAK" &
pid=$!
trap 'kill -9 $pid 2>/dev/null; pkill -9 -f "leakprobe make $PROJECT $LEAK" 2>/dev/null' EXIT

# Waits for the dataset to exist rather than sleeping a guessed number of
# seconds: BigQuery takes two to eight for this, and a fixed wait is either
# slow or flaky. `make` prints nothing until it is ready, so a poll of the
# catalog is the honest signal.
ready=0
for _ in $(seq 1 45); do
    if probe check "$PROJECT" "$LEAK" >/dev/null 2>&1; then ready=1; break; fi
    sleep 2
done
[ "$ready" -eq 1 ] || true   # not fatal: `check` below reports it properly

# --- killed, the way a closing laptop kills it ----------------------------
kill -9 $pid 2>/dev/null
# `go run` is the PARENT of the binary, and the child outlives a kill of the
# parent -- which is exactly the shape that makes this realistic.
pkill -9 -f "leakprobe make $PROJECT $LEAK" 2>/dev/null
wait $pid 2>/dev/null
echo "killed:  the cleanup never ran"
echo

# --- what is left ---------------------------------------------------------
probe check "$PROJECT" "$LEAK"
status=$?

# --- and this cleans up after itself --------------------------------------
echo
probe drop "$PROJECT" "$LEAK" || fail "the leak could not be removed; it is $LEAK in $PROJECT"

exit "$status"
