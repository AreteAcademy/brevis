// A gateway with a hook: register what this binary knows, hand it the file.
//
// The registration IS the main -- hooks and sinks both. Everything else -- the
// listener, the signals, the drain -- is gateway.Main, so a binary with code of
// its own does not re-derive them and forget one.
//
// The sinks are a LIST and not a default on purpose: this file is what somebody
// copies, and the import block is where they delete what they do not use. The
// four below are what gateway.yaml next door names; dropping bigquery and
// redshift from both takes this binary from 49 MB to 21.
//
//	PUBSUB_EMULATOR_HOST=localhost:8085 go run . gateway.yaml
package main

import (
	"strings"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/sink/bigquery"
	"github.com/AreteAcademy/brevis/gateway/sink/files"
	"github.com/AreteAcademy/brevis/gateway/sink/postgres"
	"github.com/AreteAcademy/brevis/gateway/sink/pubsub"
	"github.com/AreteAcademy/brevis/gateway/store/gcs"
)

func main() {
	hooks := gateway.NewHooks()
	hooks.MustRegister("enrich_clicks", enrichClicks)

	sinks := gateway.NewSinks()
	sinks.MustRegister(pubsub.Sink, pubsub.New)
	sinks.MustRegister(postgres.Sink, postgres.New)
	sinks.MustRegister(bigquery.Sink, bigquery.New)
	sinks.MustRegister(files.Sink, files.New)

	// gateway.yaml's telemetry stream buries into gs://, so this binary needs
	// the GCS backend. It does NOT need S3, and not registering it is the
	// difference: an s3:// path here is refused at startup, by name.
	stores := gateway.NewStores()
	stores.MustRegister(gcs.Scheme, gcs.Open)

	gateway.Main(hooks, gateway.WithSinks(sinks), gateway.WithStores(stores))
}

// enrichClicks is a hook: an ordinary Go function.
//
// Returning nil DROPS the event, which is most of what these do. An error sends
// that one event down the dead-letter path and never fails the batch beside it.
func enrichClicks(e map[string]any) (map[string]any, error) {
	host, _ := e["host"].(string)
	if host == "" {
		return e, nil
	}
	e["tenant"] = strings.Split(host, ".")[0]
	return e, nil
}
