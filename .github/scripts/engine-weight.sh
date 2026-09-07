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
for forbidden in \
  "AreteAcademy/brevis/sdk" \
  "cloud.google.com/go/bigquery" \
  "cloud.google.com/go/storage" \
  "aws/aws-sdk-go" \
  "go-sql-driver/mysql"
do
  n="$(echo "$deps" | grep -c "$forbidden" || true)"
  if [ "$n" != "0" ]; then
    echo "❌ the engine compiles $n package(s) from $forbidden"
    failed=1
  fi
done

# The ceiling on the total. It does not exist to be exact -- it exists so that
# growing takes a conscious decision instead of just happening.
CEILING="${PACKAGE_CEILING:-330}"
if [ "$total" -gt "$CEILING" ]; then
  echo "❌ the engine compiles $total packages, above the ceiling of $CEILING."
  echo "   If the growth is deliberate, raise the ceiling in this script and say why."
  failed=1
fi

if [ "$failed" = "0" ]; then
  echo "✅ engine: $total packages (ceiling $CEILING), no data driver"
fi
exit $failed
