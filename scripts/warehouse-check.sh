#!/usr/bin/env bash
#
# Every test, nothing skipped. LOCAL ONLY, and that is a decision.
#
# BigQuery is not in CI and will not be. Taken 2026-10-08, for three reasons
# and not one: this repository is PUBLIC, so a long-lived GCP key in its
# secrets is a standing target through every third-party action the workflow
# pulls; the only project on hand holds a client's real medallion datasets,
# which is not a thing to hand a test runner; and the emulator already covers
# the protocol. What the emulator CANNOT cover is the write path -- floci
# refuses load jobs by design, and a load job is how the SDK writes.
#
# So CI keeps the emulator and says so, in `Say what did not run`:
#
#   SKIPPED, which is green: nothing below this line was proven against a
#   real warehouse.
#
# THIS SCRIPT IS THE OTHER HALF OF THAT DECISION. Without it, "we run it
# locally" is a sentence rather than something that happens -- and the last
# four days of this repository are a list of things that were true in a
# sentence: nine integration tests rotted while skipped, a release shipped on
# a red commit, four CI gates could not fail.
#
# A SKIP IS A FAILURE HERE, which is the opposite of CI and is the point.
# CI tolerates a skip because it has no credentials; this run exists to prove
# everything ran, so a test that did not run is the one outcome it must
# refuse. `go test` exits 0 on a skip and prints green, so nothing else would
# notice.
set -uo pipefail

cd "$(dirname "$0")/.."

fail() { echo "::error::$*" >&2; exit 1; }

# --- what this needs, refused by name -------------------------------------
missing=()
for v in BREVIS_IT_PROJECT BREVIS_IT_DATASET BREVIS_IT_BUCKET; do
    [ -n "${!v:-}" ] || missing+=("$v")
done
if [ ${#missing[@]} -gt 0 ]; then
    cat >&2 <<MSG
This run needs a real BigQuery, and these are not set: ${missing[*]}

  export BREVIS_IT_PROJECT=my-dev-project
  export BREVIS_IT_DATASET=brevis_ci_sandbox   # must exist; give it a
                                               # default table expiry
  export BREVIS_IT_BUCKET=my-staging-bucket    # for the GCS load strategy

Credentials come from Application Default Credentials:

  gcloud auth application-default login

Point it at a project you are willing to lose. The tests CREATE AND DROP
tables and datasets.
MSG
    exit 1
fi

# --- and the containers, named with the command that starts them ----------
need_service() {
    local state
    state=$(docker compose -f docker-compose.drivers.yml ps "$1" --format '{{.State}}' 2>/dev/null)
    [ "$state" = "running" ] || fail "the container \"$1\" is not running:
    make warehouse-up"
}
for s in postgres mysql minio redis memcached; do need_service "$s"; done

# The two that answer over HTTP are polled FROM HERE. A published port is not
# a listening process, and neither container can probe itself: floci has no
# shell worth the name and the S3 mock is a bare JRE.
curl -sf -o /dev/null "http://localhost:9000/" \
    || fail "the S3 endpoint is not answering on :9000:
    make warehouse-up"

# AND THE TCP PORTS, for the same reason. `running` is what the two above
# were when they answered nothing, and a metastore that cannot be reached
# fails seven gateway tests with `connection refused` -- which reads as a
# broken test rather than as a missing container.
port_open() { # port_open <host:port> <what>
    (exec 3<>/dev/tcp/${1%%:*}/${1##*:}) 2>/dev/null \
        || fail "$2 is not answering on $1:
    make warehouse-up"
}
port_open 127.0.0.1:56379 "Redis"
port_open 127.0.0.1:51211 "memcached"
port_open 127.0.0.1:55432 "Postgres"
port_open 127.0.0.1:53306 "MySQL"
port_open 127.0.0.1:8085  "the Pub/Sub emulator"
# floci too: 16 tests use the EMULATED layer and this run refuses a skip, so
# "nothing skipped" has to include them. They are not real-warehouse tests --
# they prove the driver speaks the protocol -- and they run in CI as well.
curl -sf -o /dev/null "http://localhost:4588/bigquery/v2/projects/floci-local/datasets" \
    || fail "floci is not answering on :4588:
    make warehouse-up"

export BREVIS_IT_PG_DSN="postgres://brevis:brevis@localhost:55432/brevis_it"
export BREVIS_IT_MYSQL_DSN="root:brevis@tcp(localhost:53306)/brevis_it"
export BREVIS_IT_S3_ENDPOINT="http://localhost:9000"
export BREVIS_IT_S3_KEY="brevis"
export BREVIS_IT_S3_SECRET="brevis-secret"
export PUBSUB_EMULATOR_HOST="localhost:8085"
# BREVIS_BIGQUERY_EMULATOR is deliberately NOT exported. It points the SDK's
# BigQuery client at floci, and `./load/` holds BOTH layers -- so setting it
# globally sends the REAL-warehouse tests to the emulator, where sixteen of
# them fail on a load job floci refuses by design. CI avoids it by running
# the two in separate steps; so does this, below.
EMULATOR="http://localhost:4588"
export BREVIS_SQL_IT_DSN="$BREVIS_IT_PG_DSN"
export BREVIS_SQL_IT_BQ_PROJECT="$BREVIS_IT_PROJECT"
export BREVIS_GATEWAY_TEST_PG_DSN="$BREVIS_IT_PG_DSN"
export BREVIS_GATEWAY_TEST_MYSQL_DSN="$BREVIS_IT_MYSQL_DSN"
export BREVIS_GATEWAY_TEST_S3_ENDPOINT="$BREVIS_IT_S3_ENDPOINT"
# THE COMPOSE'S PORTS, NOT CI'S. Every service in docker-compose.drivers.yml
# is published on a shifted port -- 55432, 53306, 56379, 51211 -- so it never
# collides with a Postgres or a Redis somebody already runs. CI starts the
# same images with `docker run -p 6379:6379`, so its own values are the plain
# ones, and copying them here is what made seven gateway tests fail with
# `connection refused` against containers that were up.
export BREVIS_GATEWAY_TEST_REDIS="127.0.0.1:56379"
export BREVIS_GATEWAY_TEST_MEMCACHED="127.0.0.1:51211"
export AWS_REGION="us-east-1"
export AWS_ACCESS_KEY_ID="$BREVIS_IT_S3_KEY"
export AWS_SECRET_ACCESS_KEY="$BREVIS_IT_S3_SECRET"

log=$(mktemp -d)/run.log
: > "$log"
status=0
RUN_ENV=()

run() { # run <label> <dir> <go test args...>
    local label=$1 dir=$2; shift 2
    printf '\n\033[1m%s\033[0m\n' "$label"

    # NO PIPELINE AROUND `go test`, and that is the whole shape of this
    # function.
    #
    # The first version piped it through tee and grep and read PIPESTATUS[0],
    # and reported a CLEAN RUN with two failing tests on the screen above it
    # -- the exact failure this file exists to prevent, in the file that
    # prevents it. The cause took three attempts to pin because it only
    # happens with `set -o pipefail`, which is on at the top: pipefail makes
    # the pipeline's status the failing one, so the trailing `|| true` --
    # there to stop grep's "no match" killing the run -- actually FIRES, and
    # firing is what resets PIPESTATUS to `true`'s. Without pipefail the
    # same five lines work, which is why two isolated probes of it passed.
    local out rc=0
    out=$(mktemp)
    ( cd "$dir" && env "${RUN_ENV[@]}" go test -v -count=1 -timeout 30m "$@" ) > "$out" 2>&1 || rc=$?
    RUN_ENV=()
    cat "$out" >> "$log"
    # SILENCE IS REPORTED. A suite that fails before its first test -- a
    # build error, a bad `env` invocation -- produces no line this grep
    # knows, and the section printed nothing at all above a failing run.
    if ! grep -E '^(--- (FAIL|SKIP)|ok|FAIL|panic)' "$out"; then
        echo "    (no test result at all; the first lines were:)"
        head -5 "$out" | sed 's/^/    /'
    fi
    rm -f "$out"
    [ "$rc" -eq 0 ] || status=1
}

# TWO LAYERS, TWO ENVIRONMENTS, and they are kept apart the way CI keeps
# them apart. `./load/` holds the emulated suite and the real one; the
# emulator variable is what tells the SDK which warehouse to talk to, and
# one setting cannot be right for both.
#
# `-run` and `-skip` rather than two directories, because the two live in
# one package: floci's are all `TestIntegrationBigQuery*` and nothing else
# is. A test filtered out this way prints no `--- SKIP`, so neither pass
# reports the other's tests as having not run.
# `env` wants its OPTIONS BEFORE the assignments. With the assignment first,
# `-u` is read as the command to run and the whole invocation dies before
# `go` is reached -- which printed nothing at all and failed the run, since
# the grep below had no line to match.
RUN_ENV=(-u BREVIS_IT_PROJECT -u BREVIS_IT_DATASET -u BREVIS_IT_BUCKET
         "BREVIS_BIGQUERY_EMULATOR=$EMULATOR")
run "SDK — the EMULATED BigQuery layer (floci)" sdk -run '^TestIntegrationBigQuery' ./load/

run "SDK — the REAL warehouse: BigQuery and GCS" sdk -skip '^TestIntegrationBigQuery' ./load/... ./from/...
run "SDK — Postgres and MySQL"                   sdk ./to/postgres/ ./to/mysql/
run "brevis-sql — both dialects"                 sql ./...
run "gateway"                                    gateway ./...

# --- the teeth ------------------------------------------------------------
#
# A SKIP IS ALLOWED ONLY BY NAME. Two tests here cannot run anywhere, for
# reasons that are about the warehouse rather than about this machine, and
# they are listed so that each one is a decision somebody wrote down rather
# than a line in a log nobody reads. Anything NOT on this list fails.
#
# It is deliberately not a pattern. `TestIntegration*` would swallow the
# next twenty tests that quietly stop running.
allowed_skip() {
    case "$1" in
    # MySQL cannot UNIQUE a LONGTEXT, so a merging landing table cannot be
    # created there at all. The test says so at length; see landing_test.go.
    TestIntegrationTheLandingLayoutMergesOnMySQL) return 0 ;;
    # memcached claims one key at a time -- the per-key fallback IS its
    # implementation, so there is no bulk answer to assert an order on.
    TestABulkClaimAnswersPerKeyInOrder/memcached) return 0 ;;
    *) return 1 ;;
    esac
}

# Subtests COUNT. `^--- SKIP` anchors to the left margin and a subtest's is
# indented, so the first version of this reported three skips and printed
# four -- the count and the list disagreeing about what a test is.
mapfile -t skips < <(grep -oE -- '--- SKIP: [^ ]+' "$log" | sed 's/--- SKIP: //' | sort -u)
unexpected=()
for name in "${skips[@]:-}"; do
    [ -n "$name" ] || continue
    allowed_skip "$name" || unexpected+=("$name")
done

if [ ${#skips[@]} -gt 0 ] && [ -n "${skips[0]}" ]; then
    echo
    echo "Skipped by design (${#skips[@]} total, ${#unexpected[@]} unexpected):"
    for name in "${skips[@]}"; do
        allowed_skip "$name" && printf '    %s\n' "$name"
    done
fi

skipped=${#unexpected[@]}
if [ "$skipped" -gt 0 ]; then
    echo
    echo "::error::$skipped test(s) did not run, and this script exists so that cannot pass:"
    # THE REASON COMES BEFORE THE SKIP LINE, not after. `go test -v` prints
    # the t.Skip message, then `--- SKIP`, then the next `=== RUN` -- so the
    # obvious `grep -A1` reports the name of the test that ran NEXT, which
    # is what the first version of this did.
    # THE REASON COMES BEFORE THE SKIP LINE, not after. `go test -v` prints
    # the t.Skip message, then `--- SKIP`, then the next `=== RUN` -- so the
    # obvious `grep -A1` reports the name of the test that ran NEXT, which
    # is what the first version of this did. And when the line before is
    # another result line, the test skipped silently: say that rather than
    # print somebody else's PASS as a reason.
    for name in "${unexpected[@]}"; do
        reason=$(awk -v want="--- SKIP: $name " \
            'index($0, want) { sub(/^[ \t]+/, "", prev); print prev; exit } { prev=$0 }' "$log")
        case "$reason" in
        ---*|===*|"") reason="(it skipped without saying why)" ;;
        esac
        printf '    %-52s %s\n' "$name" "$reason"
    done
    echo
    echo "    Each one names what it wanted. A skip prints green and \`go test\`"
    echo "    exits 0, which is how nine of these rotted unnoticed."
    status=1
fi

passed=$(grep -c '^--- PASS' "$log" || true)
echo
if [ "$status" -eq 0 ]; then
    echo "$passed tests ran against the real thing. ${#skips[@]} skipped, all of them by name."
    echo "Project: $BREVIS_IT_PROJECT  Dataset: $BREVIS_IT_DATASET  Bucket: $BREVIS_IT_BUCKET"
else
    echo "$passed passed, $skipped skipped. Not a clean warehouse run."
fi
exit "$status"
