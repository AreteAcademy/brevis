#!/usr/bin/env bash
# Builds a clean consumer of the SDK -- no replace, none of the repo's tests,
# the way somebody running `go get` sees it.
#
# One argument: the version to require. Pass "local" to point the replace at the
# working tree (the gate before the tag); pass a published version to prove what
# the proxy serves (the check after the tag).
#
#   .github/scripts/consumer-check.sh local
#   .github/scripts/consumer-check.sh v0.25.0
#
# It lives in one file, and not inline in two jobs, because the two copies fell
# behind v0.17.1's API and failed nine publishes in a row. One of them was fixed
# and the other stayed red -- which is the argument against two copies, written
# by them.
set -euo pipefail

MODULO="github.com/AreteAcademy/brevis/sdk"
VERSION="${1:?usage: consumer-check.sh <version|local>}"
TREE="${2:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../sdk" && pwd)}"

DIR="$(mktemp -d)"
trap 'rm -rf "$DIR"' EXIT
cd "$DIR"

if [ "$VERSION" = "local" ]; then
  cat > go.mod <<EOF
module example.com/consumer

go 1.23

require $MODULO v0.0.0

replace $MODULO => $TREE
EOF
else
  cat > go.mod <<EOF
module example.com/consumidor

go 1.23

require $MODULO $VERSION
EOF
fi

cat > main.go <<EOF
package main

import (
	"context"
	"fmt"
	"time"

	"$MODULO"
	"$MODULO/from"
	"$MODULO/to"
	"$MODULO/store/gcs"
	"$MODULO/to/bigquery"
)

var _ = from.Refresh{Store: gcs.Credential{Bucket: "b", Object: "o"}}

// It touches the front door and one driver on each side, so a rename that
// breaks a caller fails here and not after the release.
func main() {
	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.HTTP{
			URL:     "http://x",
			PageKey: "page",
			DataKey: "results",
			// An Applier declared as a func instead of a var would not be
			// assignable here -- and only an outside consumer catches that.
			Auth: &from.Credential{
				Value: from.FromEnv("APP_SESSION"),
				Apply: from.AsCookie,
				TTL:   time.Hour,
				Refresh: &from.Refresh{
					URL:       "http://x/session",
					ExpiresAt: from.JSONField("expires"),
					WarnAfter: 7 * 24 * time.Hour,
					// Both stores, BY VALUE, as the docs show: if either
					// stopped satisfying CredentialStore, only a consumer
					// from outside the module would catch it.
					Store: from.FileStore{Name: "app-session"},
				},
			},
			Records: func(r sdk.Response) ([]any, error) {
				doc, err := r.Object()
				if err != nil {
					return nil, err
				}
				return sdk.ArrayAt("results")(doc)
			},
		},
		Preview: 5,
	})
	if err != nil {
		return
	}

	data = sdk.Transform(data,
		sdk.Accept("id"),
		sdk.Rename(map[string]string{"id": "key"}),
		sdk.Compute("provider", func(map[string]any) (any, error) { return "p", nil }),
		sdk.Compute("entity", func(map[string]any) (any, error) { return "e", nil }),
		sdk.Compute("source_key", func(r map[string]any) (any, error) { return sdk.Key("key")(r) }),
		sdk.IngestionID("provider", "entity", "source_key", "key"),
		sdk.IngestionLoadedAt(),
	)

	_, _ = sdk.Load(context.Background(), data, sdk.Target{
		To:      bigquery.Table{Dataset: "bronze", Name: "t"},
		Columns: []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "key"},
		Dedup:   sdk.DedupNone,
	})

	// The other destination, which must not drag BigQuery along with it.
	_, _ = sdk.Load(context.Background(), data, sdk.Target{To: to.Files{Path: "./out/"}})

	env := sdk.Envelope{Provider: "p", Entity: "e", SourceKey: "k"}
	id, err := env.IngestionID()
	fmt.Println(id, err, sdk.LogLevel())
}
EOF

GOFLAGS=-mod=mod go mod tidy
go build ./...
echo "✅ a clean consumer compiles against $VERSION"
