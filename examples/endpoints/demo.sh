#!/usr/bin/env bash
# The cycle the endpoints exist for, end to end, against `make up-data`:
# ingest through the gateway, build a model, answer from the warehouse, then
# from the cache, then -- once the model is rebuilt -- from the warehouse
# again. Run it with `make endpoints-demo`.
set -euo pipefail
cd "$(dirname "$0")/../.."

: "${WAREHOUSE_DSN:=postgres://brevis:brevis@localhost:${BREVIS_WAREHOUSE_PORT:-55434}/warehouse?sslmode=disable}"
: "${BREVIS_CACHE_ADDR:=127.0.0.1:56379}"
GATEWAY="http://localhost:${BREVIS_GATEWAY_PORT:-8090}"
API="http://127.0.0.1:8096"
export WAREHOUSE_DSN BREVIS_CACHE_ADDR BREVIS_ENV=local

curl -sf "$GATEWAY/health" >/dev/null || { echo "the gateway is not up at $GATEWAY: run make up-data" >&2; exit 1; }
docker compose -f docker-compose.drivers.yml --profile metastore up -d redis >/dev/null

(cd sql && go build -o ../bin/brevis-sql ./cmd/brevis-sql && go build -o ../bin/brevis-sql-api ./cmd/brevis-sql-api)

order() { # order_id sku quantity total
  curl -sf -X POST "$GATEWAY/v1/orders" -H 'Content-Type: application/json' \
    -d "{\"order_id\":\"$1\",\"placed_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"sku\":\"$2\",\"quantity\":$3,\"total\":$4}" >/dev/null
}
build() {
  ./bin/brevis-sql build --project examples/endpoints/project --dialect postgres \
    --dsn-from WAREHOUSE_DSN --generations-from BREVIS_CACHE_ADDR | grep -E "generation|models built|warning"
}
ask() { # prints the cache header and the day's units for DEMO-1
  curl -s -D - "$API/v1/orders?period=today&sku=DEMO-1" -o /tmp/brevis-endpoints-demo.json | tr -d '\r' | grep -i '^x-brevis-cache'
  echo "  body: $(cat /tmp/brevis-endpoints-demo.json)"
}

run=$(date +%s)
echo "1. two orders through the gateway"
order "demo-$run-a" DEMO-1 2 20.00
order "demo-$run-b" DEMO-1 1 10.00
sleep 2 # the stream flushes every second

echo "2. build marts.orders_daily, writing its generation"
build

./bin/brevis-sql-api serve --config examples/endpoints/api.yaml --connections examples/endpoints/brevis.yaml \
  --addr 127.0.0.1:8096 --metrics-addr 127.0.0.1:9196 \
  > /tmp/brevis-endpoints-demo.log 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do curl -sf "$API/health" >/dev/null && break; sleep 0.1; done

echo "3. ask twice: the warehouse, then the cache"
ask
ask

echo "4. a third order, and the model rebuilt"
order "demo-$run-c" DEMO-1 4 40.00
sleep 2
build

echo "5. ask again: a new generation is a miss, with the new order in it"
ask
