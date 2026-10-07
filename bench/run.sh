#!/usr/bin/env bash
# One benchmark run: bring up the destination, start the gateway, load it with
# k6, scrape the gateway, write the report.
#
#   ./bench/run.sh                      # the local build
#   BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
#
# The image form is the one that counts. A benchmark of a binary somebody built
# on their laptop measures that laptop's toolchain; the published image is what
# anybody else can reproduce, and it is what the report names.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
results="$here/results"
mkdir -p "$results"

# The inputs of the report go FIRST, before anything can fail to write one.
#
# They are committed, so without this a run whose k6 could not write its
# summary produces a report from the PREVIOUS run and exits 0. That happened:
# k6 runs as a non-root user, the mounted directory was the runner's, the write
# was denied, and CI published a green benchmark carrying a laptop's numbers
# from the day before. A stale number that looks fresh is worse than no number.
rm -f "$results/k6.json" "$results/metrics.txt" "$results/run.json"

NET=brevis-bench
PG=brevis-bench-pg
GW=brevis-bench-gw
KEY=bench
DSN_IN="postgres://brevis:brevis@$PG:5432/brevis?sslmode=disable"

PORT=${BREVIS_BENCH_PORT:-8080}
MPORT=${BREVIS_BENCH_METRICS_PORT:-9090}

# A port already listening is somebody else's gateway, and k6 will happily
# load it and report a number. That has happened: a gateway left running from
# an earlier session answered on :8080 and :9090, the run under test failed to
# bind, and the report came back with 73,093 requests, 0 events accepted and a
# stream name from a config nobody in this run had written.
#
# Refused rather than worked around. A benchmark that measures the wrong
# process is worse than one that does not run.
for port in "$PORT" "$MPORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $port is already listening, and this run would measure whatever" >&2
    echo "is on it. Stop it, or set BREVIS_BENCH_PORT / BREVIS_BENCH_METRICS_PORT:" >&2
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >&2
    exit 2
  fi
done

VUS=${BREVIS_BENCH_VUS:-16}
PER=${BREVIS_BENCH_PER_REQUEST:-200}
HOLD=${BREVIS_BENCH_HOLD:-30s}
TABLES=${BREVIS_BENCH_TABLES:-1}
IMAGE=${BREVIS_BENCH_IMAGE:-}
# A gateway binary built elsewhere. The point is comparing two commits without
# also comparing two harnesses: build both, run both through this one.
BIN=${BREVIS_BENCH_BIN:-}

cleanup() {
  [ -f "$here/.gwpid" ] && kill "$(cat "$here/.gwpid")" 2>/dev/null || true
  rm -f "$here/.gwpid"
  docker rm -f "$GW" "$PG" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> a destination"
docker network create "$NET" >/dev/null 2>&1 || true
# On the network AND on a host port: the gateway reaches it by container name
# when it is a container, and by localhost when it is a local build. One
# Postgres either way -- starting it twice is how a run measures a database
# that was still recovering.
docker run -d --name "$PG" --network "$NET" -p 55433:5432 \
  -e POSTGRES_PASSWORD=brevis -e POSTGRES_USER=brevis -e POSTGRES_DB=brevis \
  postgres:17-alpine >/dev/null
until docker exec "$PG" pg_isready -U brevis >/dev/null 2>&1; do sleep 1; done

echo "==> the gateway"
if [ -n "$IMAGE" ]; then
  docker pull -q "$IMAGE" >/dev/null
  docker run -d --name "$GW" --network "$NET" -p "$PORT":8080 -p "$MPORT":9090 \
    -e BREVIS_ENV=prod -e BREVIS_BENCH_KEYS="$KEY" -e BREVIS_BENCH_DSN="$DSN_IN" \
    -v "$here/gateway.yaml:/etc/brevis/gateway.yaml:ro" \
    "$IMAGE" >/dev/null
  label="$IMAGE"
else
  if [ -n "$BIN" ]; then
    gw="$BIN"
    label="(prebuilt: $(basename "$BIN"))"
  else
    ( cd "$root/gateway" && go build -o "$here/.gw" ./cmd/gateway )
    gw="$here/.gw"
    label="(local build)"
  fi
  # The config it actually ran with, written beside the report: the ports
  # move when the defaults are taken, and a report should not leave anybody
  # guessing which file produced it.
  sed -e "s|\"\:8080\"|\":$PORT\"|" -e "s|\"\:9090\"|\":$MPORT\"|" \
    "$here/gateway.yaml" > "$results/gateway.used.yaml"
  BREVIS_ENV=prod BREVIS_BENCH_KEYS="$KEY" \
    BREVIS_BENCH_DSN="postgres://brevis:brevis@localhost:55433/brevis?sslmode=disable" \
    "$gw" "$results/gateway.used.yaml" >"$results/gateway.log" 2>&1 &
  echo $! > "$here/.gwpid"
fi

ready=0
for i in $(seq 1 40); do
  curl -sf -o /dev/null "http://localhost:$MPORT/metrics" && { ready=1; break; }
  sleep 1
done
if [ "$ready" != 1 ]; then
  echo "the gateway never served /metrics on :$MPORT" >&2
  tail -20 "$results/gateway.log" >&2 2>/dev/null || true
  exit 2
fi

echo "==> k6: $VUS VUs, $PER events per request, hold $HOLD"
docker run --rm -i \
  --user "$(id -u):$(id -g)" \
  --add-host=host.docker.internal:host-gateway \
  -v "$here:/scripts:ro" -v "$results:/results" \
  -e BREVIS_BENCH_KEY="$KEY" \
  -e BREVIS_BENCH_URL="http://host.docker.internal:$PORT/v1/ingestion" \
  -e BREVIS_BENCH_VUS="$VUS" \
  -e BREVIS_BENCH_PER_REQUEST="$PER" \
  -e BREVIS_BENCH_HOLD="$HOLD" \
  -e BREVIS_BENCH_TABLES="$TABLES" \
  grafana/k6:latest run /scripts/gateway.js || k6_exit=$?
k6_exit=${k6_exit:-0}

echo "==> letting the buffer drain"
# The gateway answers 202 before it writes, so a scrape taken the instant k6
# stops reports a delivery that has not happened. Wait for the buffer AND the
# queue, because they are two numbers and only one of them is the buffer.
for i in $(seq 1 120); do
  b=$(curl -s localhost:$MPORT/metrics | awk '/^brevis_gateway_buffer_records/{s+=$2} /^brevis_gateway_queue_batches/{s+=$2} END{print s+0}')
  [ "${b:-1}" = "0" ] && break
  sleep 1
done

echo "==> scraping the gateway"
curl -s localhost:$MPORT/metrics > "$results/metrics.txt"
# COUNT(*), not n_live_tup. The third witness is the only one that did not
# take the gateway's word for it, and `n_live_tup` is an ESTIMATE maintained
# by the stats collector -- with one table it happened to be exact, and with
# 94 it reported 6,447,774 of 6,449,600, which reads as 1,826 lost events and
# was not. A witness that is approximately right is not a witness.
rows=$(docker exec "$PG" psql -U brevis -d brevis -t -A -c \
  "select coalesce(sum(n),0) from (
     select (xpath('/row/c/text()',
       query_to_xml(format('select count(*) as c from %I.%I', schemaname, relname),
       false, true, '')))[1]::text::bigint as n
     from pg_stat_user_tables where relname like 'app_bench%'
   ) t" 2>/dev/null || echo 0)

cat > "$results/run.json" <<JSON
{
  "image": "$label",
  "host": "$(uname -s) $(uname -m), $(getconf _NPROCESSORS_ONLN 2>/dev/null || echo '?') cpu",
  "sink": "auto_table[columns] -> postgres:17 (container), write: append",
  "rows_in_postgres": ${rows:-0}
}
JSON

echo "==> the report"
python3 "$here/report.py" "$results" > "$results/REPORT.md"
echo
sed -n '1,40p' "$results/REPORT.md"
echo
echo "full report: bench/results/REPORT.md"
exit $k6_exit
