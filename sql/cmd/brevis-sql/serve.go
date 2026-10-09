package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/connections"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	// Named, because the command is also called `serve`: the function is the
	// verb somebody types and the package is where the rules live.
	svc "github.com/AreteAcademy/brevis/sql/internal/serve"
)

// serve answers read-only previews over HTTP.
//
// IT EXISTS SO THE CONSOLE HOLDS NO WAREHOUSE CREDENTIAL. The engine forwards
// a request here; this decides what may run, bounds it, runs it and says what
// it did. Every limit lives on this side, because a limit the caller could
// change is not a limit.
//
// THE TOKEN IS NAMED BY THE ENVIRONMENT AND NEVER BY A FLAG -- the rule
// `--dsn-from` already applies to a DSN, for the same reason: a command line
// is in a shell history, in a CI log and in anybody's `ps`.
func serve(out io.Writer, addr string, rows int, maxBytes int64, registry string, dryRun bool) error {
	reg, err := connections.Load(registry)
	if err != nil {
		return err
	}
	s, err := svc.New(serveOptions(out, rows, maxBytes, reg))
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "brevis-sql serve on %s, at most %d row(s) and %s per query\n",
		addr, rows, svc.BytesText(maxBytes))
	if names := reg.Names(); len(names) > 0 {
		_, _ = fmt.Fprintf(out, "  %d declared connection(s): %s\n", len(names), strings.Join(names, ", "))
	} else {
		// SAID WHEN THERE ARE NONE, because that is the state somebody will
		// not understand: every `postgres://` destination answers "no
		// connection is declared", and the reason is a file that is not
		// there rather than anything about the warehouse.
		_, _ = fmt.Fprintf(out, "  no connections declared (%s): BigQuery destinations still work, "+
			"every other dialect answers that nothing is declared\n", registry)
	}
	_, _ = fmt.Fprintln(out, "  one audit line per query, on this stream, holding no SQL")
	_, _ = fmt.Fprintln(out, "  each warehouse is asked once whether its credential can write,",
		"and outside BREVIS_ENV=local one that can is refused")
	if os.Getenv("BREVIS_SQL_SERVE_TOKEN") == "" {
		// SAID OUT LOUD, every time. New() already refused this outside
		// local, so reaching here means somebody meant it -- and a line on
		// the screen is what stops `BREVIS_ENV=local` reaching a machine
		// other people can also reach.
		_, _ = fmt.Fprintln(out, "  no token: anybody who reaches this port can read the warehouse")
	}
	if dryRun {
		// Built and reported, nothing listening. It is what a test asserts
		// against, and what somebody runs to find out whether their
		// environment is configured before a deploy does it for them.
		return nil
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
		// A slow-header client otherwise holds a connection open for free.
		// The gateway learned this as a defect worth its own release; this
		// starts with it.
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	return srv.Serve(ln)
}

// serveOptions is what the service is built from.
//
// A FUNCTION SO THE WIRING CAN BE ASSERTED. An audit nobody connected is an
// audit that is silent exactly when it is read, and "we thought it was on"
// is the sentence this repository has already paid for once.
func serveOptions(out io.Writer, rows int, maxBytes int64, reg *connections.Registry) svc.Options {
	return svc.Options{
		// Read from the environment, never from an argument: something that
		// could declare itself local would be something that turns off
		// authentication. The gateway states the rule; this obeys it.
		Env:   os.Getenv("BREVIS_ENV"),
		Token: os.Getenv("BREVIS_SQL_SERVE_TOKEN"),
		Rows:  rows,
		Bytes: maxBytes,
		// ONE STREAM. A container gives you one anyway, and an audit line
		// somewhere else is an audit line nobody collects.
		Audit: out,
		Open:  opener(reg),
	}
}

// opener turns a target into a connection.
//
// THE REGISTRY FIRST, AND BIGQUERY STILL WORKS WITHOUT ONE. `serve` shipped
// in V1 and V2 with no configuration at all -- a `bigquery://` target names
// its project, the project IS the connection and ADC does the rest -- and a
// registry that suddenly made that a refusal would break every deployment
// that already works, to no end: those projects are ones Brevis has already
// written to, which is how they reached the catalog.
//
// Every other dialect has to be DECLARED, because a `postgres://` target
// names a database and no host, port or user. A target is an identity and
// never an address.
func opener(reg *connections.Registry) func(context.Context, svc.Table) (dialect.Conn, error) {
	return func(ctx context.Context, t svc.Table) (dialect.Conn, error) {
		if _, found := reg.Match(t.Dialect, t.Connection); found {
			return reg.Open(ctx, t.Dialect, t.Connection)
		}
		if t.Dialect == (bigquery.Dialect{}).Name() {
			return openBigQuery(ctx, t.Connection)
		}
		// NOT FOUND IS AN ANSWER, and this is where it becomes one a screen
		// can show.
		return nil, svc.ErrNoConnection
	}
}

// openBigQuery is the only connector this service has today.
//
// The connection name IS the project, which is what a `bigquery://` target
// carries. A Postgres target names a database and no host, so it needs a
// connection somebody declared -- a later slice, and the reason this is a
// function rather than a map with one entry.
func openBigQuery(ctx context.Context, connection string) (dialect.Conn, error) {
	return bigquery.Dialect{}.Open(ctx, connection)
}
