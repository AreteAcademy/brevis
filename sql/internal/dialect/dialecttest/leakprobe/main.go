// Command leakprobe is the other half of `scripts/leak-check.sh`: a harness
// that never cleans up, and the questions you ask BigQuery afterwards.
//
//	leakprobe make  <project> <schema>   create it the way a harness does, then block
//	leakprobe check <project> <schema>   what survived, and does it expire
//	leakprobe drop  <project> <schema>   remove it, and ONLY if this made it
//
// IN GO AND NOT IN `bq`. The first version of the script shelled out, and `bq`
// asked for an interactive re-auth it could not get -- while every test in this
// repository was talking to the same project happily, because they use
// Application Default Credentials through the Go client. One credential path
// for everything that reaches a warehouse here.
//
// IT GOES THROUGH THE SHARED HELPER rather than issuing its own statements, so
// a harness that stopped making its schema disposable makes this fail too. A
// probe with its own copy of the thing under test proves only the copy.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/dialecttest"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: leakprobe <make|check|drop> <project> <schema>")
		os.Exit(2)
	}
	mode, project, schema := os.Args[1], os.Args[2], os.Args[3]

	if err := run(mode, project, schema); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(mode, project, schema string) error {
	ctx := context.Background()
	d := bigquery.Dialect{}
	conn, err := d.Open(ctx, project)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	switch mode {
	case "make":
		return makeAndBlock(ctx, d, conn, schema)
	case "check":
		return check(ctx, conn, schema)
	case "drop":
		return drop(ctx, conn, schema)
	}
	return fmt.Errorf("%q is not a mode", mode)
}

// makeAndBlock is the harness that is about to be killed.
func makeAndBlock(ctx context.Context, d dialect.Dialect, conn dialect.Conn, schema string) error {
	if err := dialecttest.MakeThrowawaySchema(ctx, d, conn, schema); err != nil {
		return err
	}
	// One table, so the leak holds DATA -- which is what a harness killed
	// mid-run leaves, and what makes "the tables expire" an assertion about
	// something rather than about an empty shell.
	if err := conn.Exec(ctx, "CREATE TABLE "+schema+".t AS SELECT 1 AS n"); err != nil {
		return fmt.Errorf("building in it: %w", err)
	}
	fmt.Println("ready")
	// Long enough that the script always wins the race, short enough that a
	// forgotten one is not a process somebody finds next week.
	time.Sleep(10 * time.Minute)
	return nil
}

// check reports what the kill left behind, and fails when it would last.
func check(ctx context.Context, conn dialect.Conn, schema string) error {
	n, err := conn.Scalar(ctx, fmt.Sprintf(
		"SELECT COUNT(*) FROM `region-us`.INFORMATION_SCHEMA.SCHEMATA WHERE schema_name = '%s'",
		schema))
	if err != nil {
		return fmt.Errorf("asking whether it is there: %w", err)
	}
	if fmt.Sprint(n) != "1" {
		return fmt.Errorf("%s was never created, so this proved nothing", schema)
	}

	days, err := conn.Scalar(ctx, fmt.Sprintf(
		"SELECT option_value FROM `region-us`.INFORMATION_SCHEMA.SCHEMATA_OPTIONS\n"+
			" WHERE schema_name = '%s' AND option_name = 'default_table_expiration_days'",
		schema))
	if err != nil {
		return fmt.Errorf("asking when its tables go: %w", err)
	}
	tables, err := conn.Scalar(ctx, fmt.Sprintf(
		"SELECT COUNT(*) FROM %s.INFORMATION_SCHEMA.TABLES", schema))
	if err != nil {
		return fmt.Errorf("counting what it holds: %w", err)
	}

	if days == nil || fmt.Sprint(days) == "" {
		return fmt.Errorf("%s survived the kill holding %v table(s) and NOTHING will remove it.\n"+
			"    A harness killed in the real world leaves this beside the client's own\n"+
			"    datasets, forever. dialecttest.MakeThrowawaySchema is what should have\n"+
			"    stopped it", schema, tables)
	}

	fmt.Printf("%s survived the kill holding %v table(s), and they expire in %v day(s).\n",
		schema, tables, days)
	fmt.Println("The DATASET does not expire -- BigQuery has no such option -- so what is")
	fmt.Println("left tomorrow is an empty shell. That residual is the known one.")
	return nil
}

// drop removes the leak, and REFUSES ANY NAME THIS DID NOT MAKE.
//
// The guard is here and not in the shell because here it can be read, and
// because a prefix check written in bash is one unquoted variable from
// `DROP SCHEMA` against whatever the shell expanded. `bvs_kill_` is the
// prefix `leak-check.sh` generates and nothing else in this project uses.
func drop(ctx context.Context, conn dialect.Conn, schema string) error {
	const made = "bvs_kill_"
	if !strings.HasPrefix(schema, made) {
		return fmt.Errorf("refusing to drop %q: this removes only what it made, "+
			"and that is named %s*", schema, made)
	}
	if err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		return fmt.Errorf("removing %s: %w", schema, err)
	}
	fmt.Printf("removed %s\n", schema)
	return nil
}
