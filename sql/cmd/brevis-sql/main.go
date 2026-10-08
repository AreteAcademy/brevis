// brevis-sql turns plain `.sql` files into tables and views, in dependency
// order, as an ordinary Brevis step.
//
//	brevis-sql compile              # parse everything, connect to nothing
//	brevis-sql graph                # the inferred edges, so a wrong one is seen
//	brevis-sql graph --select silver.totals+
//	brevis-sql build --dsn-from BREVIS_SQL_DSN
//	brevis-sql test  --dsn-from BREVIS_SQL_DSN
//
// A module and a binary of its own, because anything holding a warehouse
// driver is -- the engine imports none of this and its weight gate does not
// move.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/check"
	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
	"github.com/AreteAcademy/brevis/sql/internal/project"
	runner "github.com/AreteAcademy/brevis/sql/internal/run"
)

// dialects is every warehouse this binary can build against.
//
// A MAP AND NOT A SWITCH, so the error can list what there is. "snowflake is
// not a dialect" leaves somebody guessing the spelling of the one they
// wanted.
var dialects = map[string]dialect.Dialect{
	postgres.Dialect{}.Name(): postgres.Dialect{},
	bigquery.Dialect{}.Name(): bigquery.Dialect{},
}

func known() string {
	names := make([]string, 0, len(dialects))
	for n := range dialects {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "brevis-sql:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("say which command")
	}
	cmd, rest := args[0], args[1:]

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	root := fs.String("project", ".", "the directory holding models/")
	dialectName := fs.String("dialect", "postgres", "which warehouse; it decides how references are read AND how models are built")
	sel := fs.String("select", "", "one model, or `name+` for it and everything downstream")
	dsnFrom := fs.String("dsn-from", "", "the NAME of the environment variable holding the connection string")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	switch cmd {
	case "compile", "graph", "build", "test":
	default:
		usage()
		return fmt.Errorf("%q is not a command", cmd)
	}

	// Checked BEFORE the project is read, so a wrong dialect is one message
	// and not a pile of parse errors from reading BigQuery SQL as Postgres.
	d, ok := dialects[*dialectName]
	if !ok {
		return fmt.Errorf("--dialect %s: this binary builds %s", *dialectName, known())
	}

	p, err := project.Load(*root, d.Name())
	if err != nil {
		return err
	}
	order, err := p.Select(*sel)
	if err != nil {
		return err
	}

	if cmd == "compile" {
		// Compile CONNECTS TO NOTHING, which is what makes it the thing to
		// run in a pull request: every model parses, every edge resolves and
		// the order exists, without a credential anywhere.
		fmt.Fprintf(out, "%d models, %d to build\n", len(p.Models), len(order))
		for _, ref := range order {
			m := p.Models[ref]
			fmt.Fprintf(out, "  %-34s %-5s %d test(s)\n", ref, m.Materialised, len(m.Tests))
		}
		return nil
	}

	if cmd == "build" || cmd == "test" {
		conn, err := connect(d, *dsnFrom)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close(context.Background()) }()
		if cmd == "build" {
			return build(out, d, conn, p, order)
		}
		return runTests(out, d, conn, p, order)
	}

	// The INFERRED graph, printed. The extractor reads 99.3% of real Postgres
	// and this is how somebody finds the other 0.7% -- by looking, before a
	// build, rather than by a model running in the wrong order at night.
	for _, ref := range order {
		fmt.Fprintln(out, ref)
		for _, d := range p.Edges[ref] {
			fmt.Fprintf(out, "  ├─ %s\n", d)
		}
		for _, s := range p.Sources[ref] {
			fmt.Fprintf(out, "  └─ %s (source)\n", s)
		}
	}
	return nil
}

// connect resolves the DSN and opens.
//
// THE DSN IS NAMED, NEVER WRITTEN. `--dsn-from` takes the NAME of an
// environment variable -- the same split the gateway's `dsn_from` makes, for
// the same reason: a connection string carries a password, and a command
// line is in a shell history, a CI log and anybody's `ps`.
//
// ONE FUNCTION FOR BOTH COMMANDS. `test` grew the same three refusals as
// `build`, and two copies is two places for the DSN-shaped one to be
// forgotten -- which is the refusal that exists to keep a password off a
// command line.
func connect(d dialect.Dialect, dsnFrom string) (dialect.Conn, error) {
	if dsnFrom == "" {
		return nil, fmt.Errorf("this needs somewhere to connect: --dsn-from takes "+
			"the NAME of an environment variable holding the connection string, "+
			"e.g. --dsn-from BREVIS_SQL_DSN (%s)", d.Name())
	}
	// Refused by name rather than used: a DSN here would be the password in
	// the process list, and treating it as a variable name would fail later
	// with "not set", pointing at the wrong thing. The error prints the FLAG
	// and never the value.
	if strings.ContainsAny(dsnFrom, ":/@ ") {
		return nil, fmt.Errorf("--dsn-from takes the NAME of an environment variable, " +
			"not the connection string itself. The value given looks like a DSN " +
			"and is not repeated here; put it in a variable and name the variable")
	}
	dsn := os.Getenv(dsnFrom)
	if dsn == "" {
		return nil, fmt.Errorf("--dsn-from %s and %s is not set in this environment", dsnFrom, dsnFrom)
	}
	return d.Open(context.Background(), dsn)
}

// build connects and runs the models.
func build(out io.Writer, d dialect.Dialect, conn dialect.Conn, p *project.Project, order []string) error {
	ctx := context.Background()
	res, err := runner.Build(ctx, d, conn, p, order)
	// REPORTED EVEN WHEN IT FAILED. The build stops at the first refusal, and
	// what ran before it is what somebody needs to know to decide whether to
	// re-run or to repair.
	for _, b := range res.Built {
		was := string(b.Was)
		if was == "" {
			was = "new"
		}
		fmt.Fprintf(out, "  %-34s %-5s %-5s %s\n", b.Ref, b.Kind, was, b.Took.Round(time.Millisecond))
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%d models built on %s\n", len(res.Built), d.Name())
	return nil
}

// runTests runs every model's tests and reports the whole thing.
//
// A FAILURE NAMES THE MODEL, THE COLUMN AND A ROW, which is the difference
// between a report somebody acts on and one they have to go and reproduce.
// `not_null` has no row worth printing -- every offender is NULL -- and
// says nothing rather than printing NULL back.
//
// The query is printed with the failure. It is the thing to paste into a
// console and keep narrowing, and a test that cannot be re-run by hand is a
// test somebody argues with.
func runTests(out io.Writer, d dialect.Dialect, conn dialect.Conn, p *project.Project, order []string) error {
	res, err := check.Run(context.Background(), d, conn, p, order)
	if err != nil {
		return err
	}

	for _, f := range res.Failed {
		example := ""
		if f.Value != "" {
			example = fmt.Sprintf("  e.g. %q", f.Value)
		}
		fmt.Fprintf(out, "FAIL  %-30s %-16s %-20s %d row(s)%s\n",
			f.Model, f.Kind, f.Column, f.Rows, example)
		fmt.Fprintf(out, "      %s\n", f.Count)
	}

	if len(res.Failed) > 0 {
		// A NON-ZERO EXIT, because this runs as a Brevis step and a step
		// that reports a failure in its output and succeeds is a step the
		// workflow carries on past.
		return fmt.Errorf("%d of %d tests failed", len(res.Failed), res.Ran)
	}
	fmt.Fprintf(out, "%d tests passed on %s\n", res.Ran, d.Name())
	return nil
}

// usage DERIVES the dialect list. It used to say "postgres | bigquery" while
// the binary built one of them, which is a help text that lies -- and the
// --dialect error below would have contradicted it.
func usage() {
	fmt.Fprintf(os.Stderr, strings.TrimLeft(`
brevis-sql — plain .sql models, run as a Brevis step

  compile   parse every model, resolve every edge, connect to nothing
  graph     print the inferred edges, so a wrong one is seen
  build     create or replace every model, in dependency order
  test      run every model's tests; each one is a SELECT that must find nothing

  --project DIR     the directory holding models/   (default ".")
  --dialect NAME    %-28s (default "postgres")
  --select  EXPR    one model, or `+"`name+`"+` for it and everything downstream
  --dsn-from VAR    the NAME of the variable holding the DSN  (build, test)
`, "\n"), known())
}
