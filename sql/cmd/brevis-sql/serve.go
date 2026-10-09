package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

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
func serve(out io.Writer, addr string, rows int, dryRun bool) error {
	s, err := svc.New(svc.Options{
		// Read from the environment, never from an argument: something that
		// could declare itself local would be something that turns off
		// authentication. The gateway states the rule; this obeys it.
		Env:   os.Getenv("BREVIS_ENV"),
		Token: os.Getenv("BREVIS_SQL_SERVE_TOKEN"),
		Rows:  rows,
		Open:  openBigQuery,
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "brevis-sql serve on %s, at most %d row(s) per preview\n", addr, rows)
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

// openBigQuery is the only connector this service has today.
//
// The connection name IS the project, which is what a `bigquery://` target
// carries. A Postgres target names a database and no host, so it needs a
// connection somebody declared -- a later slice, and the reason this is a
// function rather than a map with one entry.
func openBigQuery(ctx context.Context, connection string) (dialect.Conn, error) {
	return bigquery.Dialect{}.Open(ctx, connection)
}
