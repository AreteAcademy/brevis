// Command gateway-slim is the published gateway with two sinks instead of six.
//
// Postgres and a local `files` dead letter, which is the shape most
// deployments actually have: events arrive over HTTP and land in a table, and
// what the table will not take goes to a mounted volume.
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
	"github.com/AreteAcademy/brevis/gateway/sink/files"
	"github.com/AreteAcademy/brevis/gateway/sink/postgres"
)

func main() {
	sinks := gateway.NewSinks()
	sinks.MustRegister(postgres.Sink, postgres.New)
	sinks.MustRegister(files.Sink, files.New)

	// No stores: a gs:// or s3:// path is refused at startup, naming the
	// scheme. A slim build that silently accepted one would fail on the first
	// batch it had to bury, which is the failure this whole split prevents.
	gateway.Main(nil, gateway.WithSinks(sinks))
}
