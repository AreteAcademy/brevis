// brevis-sql turns plain `.sql` files into tables and views, in dependency
// order, as an ordinary Brevis step.
//
//	brevis-sql compile              # parse everything, connect to nothing
//	brevis-sql graph                # the inferred edges, so a wrong one is seen
//	brevis-sql graph --select silver.totals+
//
// A module and a binary of its own, because anything holding a warehouse
// driver is -- the engine imports none of this and its weight gate does not
// move.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/project"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "brevis-sql:", err)
		os.Exit(1)
	}
}

func run(args []string, out *os.File) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("say which command")
	}
	cmd, rest := args[0], args[1:]

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	root := fs.String("project", ".", "the directory holding models/")
	dialect := fs.String("dialect", "postgres", "postgres or bigquery; it decides how references are read")
	sel := fs.String("select", "", "one model, or `name+` for it and everything downstream")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	switch cmd {
	case "compile", "graph":
	default:
		usage()
		return fmt.Errorf("%q is not a command", cmd)
	}

	p, err := project.Load(*root, *dialect)
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

func usage() {
	fmt.Fprint(os.Stderr, strings.TrimLeft(`
brevis-sql — plain .sql models, run as a Brevis step

  compile   parse every model, resolve every edge, connect to nothing
  graph     print the inferred edges, so a wrong one is seen

  --project DIR     the directory holding models/   (default ".")
  --dialect NAME    postgres | bigquery             (default "postgres")
  --select  EXPR    one model, or `+"`name+`"+` for it and everything downstream
`, "\n"))
}
