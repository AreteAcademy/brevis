#!/usr/bin/env bash
# Proves the engine stays light.
#
# The API and the scheduler are the SAME binary (`brevis serve` and
# `brevis scheduler`), and it compiles not one line of the SDK. That is the
# design, not an accident: whoever operates Brevis runs a process that
# orchestrates, and the data drivers -- pgx on the fetcher's side, BigQuery, S3,
# MySQL -- live in the TASKS' pods, which are other images.
#
# The root's go.mod has `replace .../sdk => ./sdk`, and today it is inert because
# nothing imports the SDK. It is a loaded gun: the day somebody imports ONE
# package of the SDK to reuse a type, the driver tree comes along and the image
# that is 7 MB today starts carrying BigQuery's SDK. Without this test nobody
# would notice -- the build stays green, only the image grows.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

deps="$(go list -deps ./cmd/brevis)"
total="$(echo "$deps" | wc -l | tr -d ' ')"
failed=0

# The whole SDK, and the world it brings with it.
#
# protobuf and client_golang are on this list as of the metrics work, and the
# reason is the ceiling below: the OpenTelemetry SDK costs 42 packages, and its
# Prometheus EXPORTER costs 49 more, of which 29 are protobuf -- dragged in
# because client_golang registers through the generated client_model. The engine
# renders the exposition itself instead (internal/observability/metrics), which
# is around a hundred lines of a format that has not changed in a decade.
#
# Without these two names the decision does not hold: somebody reaches for the
# exporter, the total lands under the ceiling anyway, and the gate says nothing
# while the image grows a serialization library it never serializes with.
# See docs/plan/2026-09-08-observability.md, section 1.
for forbidden in \
  "AreteAcademy/brevis/sdk" \
  "cloud.google.com/go/bigquery" \
  "cloud.google.com/go/storage" \
  "aws/aws-sdk-go" \
  "go-sql-driver/mysql" \
  "google.golang.org/protobuf" \
  "prometheus/client_golang"
do
  n="$(echo "$deps" | grep -c "$forbidden" || true)"
  if [ "$n" != "0" ]; then
    echo "❌ the engine compiles $n package(s) from $forbidden"
    failed=1
  fi
done

# The ceiling on the total. It does not exist to be exact -- it exists so that
# growing takes a conscious decision instead of just happening.
#
# It was 330 while the engine was 299 and had no metrics. It moved ONCE, to 360,
# to buy go.opentelemetry.io/otel/sdk/metric: measured at 341 for the union, plus
# what this repository's own metrics packages add, plus about five percent so a
# patch bump upstream does not turn CI red on its own.
#
# A number that moves whenever it is inconvenient is not a gate. If it has to
# move again, the commit that moves it says what was bought -- and the forbidden
# list above is the half of this check that has teeth either way.
CEILING="${PACKAGE_CEILING:-360}"
if [ "$total" -gt "$CEILING" ]; then
  echo "❌ the engine compiles $total packages, above the ceiling of $CEILING."
  echo "   If the growth is deliberate, raise the ceiling in this script and say why."
  failed=1
fi

if [ "$failed" = "0" ]; then
  echo "✅ engine: $total packages (ceiling $CEILING), no data driver"
fi
exit $failed
