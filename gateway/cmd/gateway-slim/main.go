// Command gateway-slim is the published gateway with two sinks instead of six.
//
// Postgres, a local `files` dead letter, and `auto_table` -- which is the shape
// most deployments actually have: events arrive over HTTP and land in a table,
// and what the table will not take goes to a mounted volume.
//
// auto_table is here because it costs nothing: it is pure Go with no client of
// its own, routing into the Postgres sink above. Leaving it out would put the
// cheapest way to land arbitrary events behind the 49 MB image.
//
// It is 10 MB against the full image's 49, and the difference is entirely
// drivers nobody in this deployment uses -- the AWS SDK, the Google client
// stack, Arrow. The linker prunes what is not imported, so the import list
// above is the whole of the configuration.
//
// A config naming `bigquery` here is refused AT STARTUP, naming what this
// binary carries: "sink type \"bigquery\" is not one this binary carries (it
// has: files, postgres)". That is the honest message -- it is a build that
// left it out, not a destination that does not exist -- and it is why the
// registry reports per binary rather than from a fixed list.
//
// Want a different pair? This file is the template: copy it, change the
// imports, build. Anyone with a hook is compiling their own binary anyway.
package main

import (
	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/sink/autotable"
	"github.com/AreteAcademy/brevis/gateway/sink/files"
	"github.com/AreteAcademy/brevis/gateway/sink/postgres"
)

func main() {
	sinks := gateway.NewSinks()
	sinks.MustRegister(postgres.Sink, postgres.New)
	sinks.MustRegister(files.Sink, files.New)
	// auto_table too, and it is free: pure Go, no cloud client, no driver of
	// its own -- it ROUTES into postgres above. Leaving it out would mean the
	// cheapest way to land arbitrary events in a table needed the 49 MB image.
	sinks.MustRegister(autotable.Sink, autotable.New)

	// No stores: a gs:// or s3:// path is refused at startup, naming the
	// scheme. A slim build that silently accepted one would fail on the first
	// batch it had to bury, which is the failure this whole split prevents.
	gateway.Main(nil, gateway.WithSinks(sinks))
}
