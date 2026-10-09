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
	"encoding/json"
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
		_, _ = fmt.Fprintln(os.Stderr, "brevis-sql:", err)
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
	full := fs.Bool("full-refresh", false, "rebuild every incremental model from scratch, forgetting its watermark")
	addr := fs.String("addr", "127.0.0.1:8088", "where `serve` listens")
	rows := fs.Int("rows", 100, "the most rows a preview may return; a caller may ask for fewer")
	maxBytes := fs.Int64("max-bytes", 10<<30, "the most bytes one query may scan; it is priced first and refused above this")
	dryRun := fs.Bool("dry-run", false, "build the service and report, without listening")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	switch cmd {
	case "compile", "graph", "build", "test":
	case "serve":
		// BEFORE THE PROJECT IS READ, and that is the whole reason it returns
		// here. Every other command loads `models/` first; this one has no
		// project, and a `brevis-sql serve` in a directory without models
		// would otherwise die with a message about a thing it does not use.
		return serve(out, *addr, *rows, *maxBytes, *dryRun)
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
		// The functions are counted too: a project whose `functions/`
		// directory is misspelt compiles to the same model list, and the
		// only way to see it is a count that says zero.
		_, _ = fmt.Fprintf(out, "%d models, %d to build, %d function(s)\n",
			len(p.Models), len(order), len(p.Functions))
		for _, ref := range order {
			m := p.Models[ref]
			_, _ = fmt.Fprintf(out, "  %-34s %-11s %d test(s)\n", ref, m.Materialised, len(m.Tests))
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
			return build(out, d, conn, p, order, runner.Options{FullRefresh: *full})
		}
		return runTests(out, d, conn, p, order)
	}

	// The INFERRED graph, printed. The extractor reads 99.3% of real Postgres
	// and this is how somebody finds the other 0.7% -- by looking, before a
	// build, rather than by a model running in the wrong order at night.
	for _, ref := range order {
		_, _ = fmt.Fprintln(out, ref)
		for _, d := range p.Edges[ref] {
			_, _ = fmt.Fprintf(out, "  ├─ %s\n", d)
		}
		for _, s := range p.Sources[ref] {
			_, _ = fmt.Fprintf(out, "  └─ %s (source)\n", s)
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
func build(out io.Writer, d dialect.Dialect, conn dialect.Conn, p *project.Project,
	order []string, opt runner.Options) error {

	ctx := context.Background()
	marks := map[string]string{}
	res, err := runner.Build(ctx, d, conn, p, order, opt)
	// REPORTED EVEN WHEN IT FAILED, and the `landed` lines with it. The
	// build stops at the first refusal, and the models before it DID land
	// -- the engine's own runner says the same of a step that landed rows
	// and then died. Swallowing them would hide real tables from `/data`
	// because a later model was broken.
	for _, b := range res.Built {
		was := string(b.Was)
		if was == "" {
			was = "new"
		}
		_, _ = fmt.Fprintf(out, "  %-34s %-11s %-5s %s\n", b.Ref, b.Kind, was, b.Took.Round(time.Millisecond))
		_, _ = fmt.Fprintln(out, landedLine(b.Target, b.Rows))
		if b.Watermark != "" {
			marks[b.Ref] = b.Watermark
		}
	}
	// ONE CONTEXT LINE FOR THE WHOLE BUILD, after the landings.
	//
	// `type: context` and not a field on `landed`: the engine's landed
	// reader knows `target`, `rows` and `bytes` and ignores anything else in
	// silence, so a watermark put there would be written by this and read by
	// nothing. The context marker is the road that exists -- "what the step
	// publishes for the steps below it, on the pipe that already exists" --
	// and the engine stores what arrives on it.
	//
	// KEYED BY MODEL, because a project builds many and a step below wants
	// the one it is about. A single `watermark` key would be whichever model
	// happened to be last in dependency order.
	//
	// Emitted only when there IS one: an empty object would clear what came
	// before, which the engine reads as a deliberate act.
	if len(marks) > 0 {
		_, _ = fmt.Fprintln(out, contextLine(marks))
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "%d models built on %s\n", len(res.Built), d.Name())
	return nil
}

// landedLine is how a step tells the engine what it wrote:
//
//	@brevis:{"type":"landed","target":"bigquery://acme-prod/bronze/clicks","rows":48213}
//
// ONE CONTRACT FOR EVERY LANGUAGE, on the pipe that already carries the
// phases. The Go SDK sends it after a load, a Python step calls `landed()`,
// and this echoes it -- which is why brevis-sql appears on `/data` beside
// everything else without the engine importing a line of this module.
//
// `rows` IS OMITTED RATHER THAN ZERO when there is no count. The engine's
// field is a pointer for that reason: "a step that does not count says
// nothing, and a nil summed into a zero would draw a table that emptied
// overnight". A view holds no rows at all; a table that really is empty
// still says 0.
//
// Marshalled rather than formatted, so a dataset with a quote in its name
// cannot produce a line the engine silently drops -- and on ONE line,
// because the pipe is read line by line and a break would split the message
// into two that the marker does not start.
func landedLine(target string, rows *int64) string {
	msg := struct {
		Type   string `json:"type"`
		Target string `json:"target"`
		Rows   *int64 `json:"rows,omitempty"`
	}{Type: "landed", Target: target, Rows: rows}

	encoded, err := json.Marshal(msg)
	if err != nil {
		// Unreachable: three fields, two strings and an int. Returning a
		// comment rather than a half-written marker, because a malformed
		// `@brevis:` line is the one thing worse than no line.
		return "# brevis-sql could not encode a landing for " + target
	}
	return "@brevis:" + string(encoded)
}

// contextLine publishes the incremental models' new watermarks:
//
//	@brevis:{"type":"context","value":{"silver.orders":"2026-03-11T04:00:00Z"}}
//
// So a step below reads where this build got to without querying the
// warehouse for it -- which is what #63 asks for, and the reason the value
// is a string the engine stores rather than a number anybody computes with.
//
// READ AFTER THE BUILD, so it is where this run got TO and not where the
// last one stopped. Those are the same number on a run that added nothing
// and different on every run that added something.
func contextLine(marks map[string]string) string {
	msg := struct {
		Type  string            `json:"type"`
		Value map[string]string `json:"value"`
	}{Type: "context", Value: marks}

	encoded, err := json.Marshal(msg)
	if err != nil {
		// Unreachable: a map of strings. A comment rather than half a
		// marker, for landedLine's reason.
		return "# brevis-sql could not encode the watermarks"
	}
	return "@brevis:" + string(encoded)
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
		_, _ = fmt.Fprintf(out, "FAIL  %-30s %-16s %-20s %d row(s)%s\n",
			f.Model, f.Kind, f.Column, f.Rows, example)
		_, _ = fmt.Fprintf(out, "      %s\n", f.Count)
	}

	if len(res.Failed) > 0 {
		// A NON-ZERO EXIT, because this runs as a Brevis step and a step
		// that reports a failure in its output and succeeds is a step the
		// workflow carries on past.
		return fmt.Errorf("%d of %d tests failed", len(res.Failed), res.Ran)
	}
	_, _ = fmt.Fprintf(out, "%d tests passed on %s\n", res.Ran, d.Name())
	return nil
}

// usage DERIVES the dialect list. It used to say "postgres | bigquery" while
// the binary built one of them, which is a help text that lies -- and the
// --dialect error below would have contradicted it.
func usage() {
	_, _ = fmt.Fprintf(os.Stderr, strings.TrimLeft(`
brevis-sql — plain .sql models, run as a Brevis step

  compile   parse every model, resolve every edge, connect to nothing
  graph     print the inferred edges, so a wrong one is seen
  build     create or replace every model, in dependency order
  test      run every model's tests; each one is a SELECT that must find nothing
  serve     answer read-only previews over HTTP, so a console needs no credential

  --project DIR     the directory holding models/   (default ".")
  --dialect NAME    %-28s (default "postgres")
  --select  EXPR    one model, or `+"`name+`"+` for it and everything downstream
  --dsn-from VAR    the NAME of the variable holding the DSN  (build, test)
  --full-refresh    rebuild every incremental model from scratch   (build)
  --addr HOST:PORT  where to listen                            (serve)
  --rows N          the most rows a preview returns            (serve)

  serve reads BREVIS_SQL_SERVE_TOKEN and BREVIS_ENV from the environment.
  Outside BREVIS_ENV=local a token is required and it will not start without
  one.
`, "\n"), known())
}
