package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/generation"
)

// writeGenerations records that each model was rebuilt, so the API's cached
// answers over it go stale now rather than at their ceiling.
//
// A WARNING, NEVER AN ERROR. The models are built and landed; a generation
// that could not be written costs a cache that serves the previous answer
// until its ttl, and a build failed over it would cost a pipeline. The
// warning names the VARIABLE and never its value, which may carry a
// password.
func writeGenerations(out io.Writer, variable string, refs []string) {
	if len(refs) == 0 {
		return
	}
	warn := func(why string) {
		_, _ = fmt.Fprintf(out, "warning: the generations of %d model(s) were not written (%s from %s); "+
			"cached answers over them stay until their ttl\n", len(refs), why, variable)
	}
	addr := os.Getenv(variable)
	if addr == "" {
		warn("nothing")
		return
	}
	c, err := generation.Open(addr)
	if err != nil {
		warn(err.Error())
		return
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := generation.Write(ctx, c, refs, generation.NewToken()); err != nil {
		warn(err.Error())
		return
	}
	_, _ = fmt.Fprintf(out, "%d generation(s) written\n", len(refs))
}

// modelRef is `schema.name`, or `project.schema.name` on BigQuery -- whose
// project may hold hyphens. generation.Model reduces either to the name a
// build writes.
var modelRef = regexp.MustCompile(`^([a-z][a-z0-9-]*[a-z0-9]\.)?[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)

// bumpGenerations is `brevis-sql generation bump`: the same write, for a
// table something other than `brevis-sql build` rebuilt -- a Go step, dbt.
//
// HERE IT IS AN ERROR, because here it is the whole job: a bump that did
// not happen and exited 0 is a step that lied.
func bumpGenerations(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "bump" {
		return fmt.Errorf("generation takes one subcommand: bump --generations-from VAR <schema.name> [<schema.name> ...]")
	}
	fs := flag.NewFlagSet("generation bump", flag.ContinueOnError)
	from := fs.String("generations-from", "", "the NAME of the variable holding the Redis address")
	dialectName := fs.String("dialect", "postgres", "how the names are read: postgres lowers them, bigquery drops a leading project")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	refs := fs.Args()
	if *from == "" || len(refs) == 0 {
		return fmt.Errorf("generation bump needs --generations-from VAR and at least one schema.name")
	}
	for i, r := range refs {
		if !modelRef.MatchString(r) {
			return fmt.Errorf("%q is not schema.name", r)
		}
		refs[i] = generation.Model(*dialectName, r)
	}
	addr := os.Getenv(*from)
	if addr == "" {
		return fmt.Errorf("--generations-from names %s, and it is empty", *from)
	}
	c, err := generation.Open(addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := generation.Write(ctx, c, refs, generation.NewToken()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "%d generation(s) written\n", len(refs))
	return nil
}
