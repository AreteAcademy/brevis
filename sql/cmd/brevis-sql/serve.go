package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
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
func serve(out io.Writer, addr string, rows int, maxBytes, budget int64, registry, metricsAddr string, dryRun bool) error {
	reg, err := connections.Load(registry)
	if err != nil {
		return err
	}
	s, err := svc.New(serveOptions(out, addr, rows, maxBytes, budget, reg))
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
	// THE BUDGET, WHICHEVER IT IS. CHECKPOINT B's F6 asked for a stated
	// choice and not a number: "for one authenticated operator [no budget]
	// may well be the right choice -- but it should be a STATED choice, and
	// today it is an absence." So the absence is said out loud, the way "no
	// token" is, and the presence carries what it cannot bound.
	if budget > 0 {
		_, _ = fmt.Fprintf(out, "  budget %s per connection per hour, over every query; "+
			"it bounds only warehouses that report bytes\n", svc.BytesText(budget))
	} else {
		_, _ = fmt.Fprintf(out, "  no budget: %s per query and nothing bounds the sum\n", svc.BytesText(maxBytes))
	}
	_, _ = fmt.Fprintln(out, "  one audit line per query, on this stream, holding no SQL")
	if metricsAddr != "" {
		_, _ = fmt.Fprintf(out, "  metrics on %s/metrics, holding no SQL and no statement hash\n", metricsAddr)
	}
	_, _ = fmt.Fprintln(out, "  each warehouse is asked once whether its credential can write,",
		"and outside BREVIS_ENV=local one that can is refused")
	// WHO MAY ASK, BY NAME. The names are not secrets -- every audit line
	// carries one -- and the tokens never leave the environment.
	if named := namedCallers(); len(named) > 0 {
		names := make([]string, 0, len(named))
		for _, c := range named {
			names = append(names, c.Name)
		}
		_, _ = fmt.Fprintf(out, "  %d named caller(s): %s — each one's audit lines carry its name\n",
			len(names), strings.Join(names, ", "))
	}
	if os.Getenv("BREVIS_SQL_SERVE_TOKEN") == "" && len(namedCallers()) == 0 {
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
	// THE EXPOSITION ON A LISTENER OF ITS OWN, which is the rule the engine
	// and the gateway both state: a scrape endpoint on the port that answers
	// requests would either need a session no scraper has, or publish every
	// connection name to whoever can reach that port.
	//
	// A failure to listen there does NOT stop the service. Metrics are how
	// somebody watches a warehouse being read; losing them is worse than not
	// having them, and refusing to serve queries over it would be worse
	// still.
	if metricsAddr != "" {
		go func() {
			ms := &http.Server{
				Addr:              metricsAddr,
				Handler:           s.Metrics(),
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := ms.ListenAndServe(); err != nil {
				_, _ = fmt.Fprintf(out, "  metrics are NOT being served on %s: %v\n", metricsAddr, err)
			}
		}()
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
func serveOptions(out io.Writer, addr string, rows int, maxBytes, budget int64, reg *connections.Registry) svc.Options {
	return svc.Options{
		// Read from the environment, never from an argument: something that
		// could declare itself local would be something that turns off
		// authentication. The gateway states the rule; this obeys it.
		Env:     os.Getenv("BREVIS_ENV"),
		Token:   os.Getenv("BREVIS_SQL_SERVE_TOKEN"),
		Callers: namedCallers(),
		// WHERE IT WILL LISTEN, handed over as evidence: an unset BREVIS_ENV
		// on an address other machines can reach is a deployment that forgot
		// a variable, and New refuses it.
		Addr:   addr,
		Rows:   rows,
		Bytes:  maxBytes,
		Budget: budget,
		// ONE STREAM. A container gives you one anyway, and an audit line
		// somewhere else is an audit line nobody collects.
		Audit: out,
		Open:  opener(reg),
	}
}

// callerPrefix is how a named token is spelled in the environment.
//
// ONE VARIABLE PER SECRET, and not one variable holding a list: a list needs
// a separator, and a separator is a character a token may not contain. It is
// also how every secret manager hands them over.
const callerPrefix = "BREVIS_SQL_SERVE_TOKEN_"

// namedCallers reads `BREVIS_SQL_SERVE_TOKEN_<NAME>` out of the environment.
//
// The NAME is the suffix, lowercased, and it is not a secret: it is printed
// at boot and written into every audit line. The VALUE never leaves here.
//
// FROM THE ENVIRONMENT AND NEVER A FLAG, which is the rule `--dsn-from`
// already applies to a DSN, for its reason: a command line is in a shell
// history, in a CI log and in anybody's `ps`.
func namedCallers() []svc.Caller {
	var out []svc.Caller
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		name, ok := strings.CutPrefix(k, callerPrefix)
		if !ok {
			continue
		}
		// An empty suffix is `BREVIS_SQL_SERVE_TOKEN_` itself, which is a
		// typo rather than a caller. It is passed through with no name so
		// the service refuses it by name at boot, where the message is.
		out = append(out, svc.Caller{Name: strings.ToLower(name), Token: v})
	}
	// SORTED, so the banner reads the same on every boot and a test can say
	// what it will contain. `os.Environ` has no promised order.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
