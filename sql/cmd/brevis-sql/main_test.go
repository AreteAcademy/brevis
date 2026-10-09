package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

const fixture = "../../internal/run/testdata/project"

func out() *bytes.Buffer { return &bytes.Buffer{} }

// `compile` CONNECTS TO NOTHING, which is what makes it the thing to run in
// a pull request. No DSN anywhere, and it still reports the whole project.
func TestCompileNeedsNoConnection(t *testing.T) {
	b := out()
	if err := run([]string{"compile", "--project", fixture}, b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bvs_run_it.base", "bvs_run_it.middle", "bvs_run_it.top"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("compile did not report %s:\n%s", want, b)
		}
	}
}

// THE DSN IS NAMED, NEVER WRITTEN. `--dsn-from` takes the name of an
// environment variable, the same split the gateway's `dsn_from` makes and
// for the same reason: a connection string carries a password and a command
// line is in a shell history, a CI log and a process list.
func TestBuildTakesTheNameOfAVariableAndNotTheDSN(t *testing.T) {
	err := run([]string{"build", "--project", fixture,
		"--dsn-from", "postgres://user:secret@host:5432/db"}, out())
	if err == nil {
		t.Fatal("a DSN passed where a variable name belongs was accepted")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the refusal printed the password back: %v", err)
	}
	if !strings.Contains(err.Error(), "--dsn-from") {
		t.Errorf("the refusal does not say what the flag takes: %v", err)
	}
}

func TestBuildWithoutADSNSaysSo(t *testing.T) {
	err := run([]string{"build", "--project", fixture}, out())
	if err == nil || !strings.Contains(err.Error(), "--dsn-from") {
		t.Errorf("a build with no connection: %v", err)
	}
}

// A variable that is not set is not the same as a flag that was not given,
// and the message says which.
func TestAnUnsetVariableNamesItself(t *testing.T) {
	const name = "BREVIS_SQL_DSN_THAT_IS_NOT_SET"
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"build", "--project", fixture, "--dsn-from", name}, out())
	if err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
}

// A dialect nothing implements is refused BY NAME, listing what there is.
// Silently falling back to Postgres would build a BigQuery project with
// Postgres DDL and report success.
func TestAnUnknownDialectIsRefusedByName(t *testing.T) {
	err := run([]string{"build", "--project", fixture,
		"--dialect", "snowflake", "--dsn-from", "NOPE"}, out())
	if err == nil {
		t.Fatal("an unimplemented dialect was accepted")
	}
	if !strings.Contains(err.Error(), "snowflake") || !strings.Contains(err.Error(), "postgres") {
		t.Errorf("the refusal does not name it and what there is: %v", err)
	}
}

// BOTH DIALECTS ARE REACHABLE FROM THE COMMAND LINE. The registry is the
// wire between `--dialect` and the code that builds, and a dialect that
// passes its conformance suite and is not in the map is a dialect nobody
// can use. A mutation removing either entry fails here.
func TestEveryDialectTheBinaryHasIsReachable(t *testing.T) {
	for _, name := range []string{"postgres", "bigquery"} {
		if _, ok := dialects[name]; !ok {
			t.Errorf("--dialect %s is not in the registry", name)
		}
	}
	// And the registry's keys ARE the dialects' own names, because `refs`
	// switches on the same string: a key that disagrees would read a
	// project's SQL as one dialect and build it as another.
	for key, d := range dialects {
		if d.Name() != key {
			t.Errorf("registered under %q and calls itself %q", key, d.Name())
		}
	}
}

// And the help lists what there is, derived. It said "postgres | bigquery"
// while the binary built one of them.
func TestTheUnknownDialectErrorListsBoth(t *testing.T) {
	err := run([]string{"build", "--project", fixture,
		"--dialect", "duckdb", "--dsn-from", "NOPE"}, out())
	if err == nil {
		t.Fatal("an unimplemented dialect was accepted")
	}
	for _, want := range []string{"duckdb", "bigquery", "postgres"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// `test` needs a connection just as `build` does, and refuses the same way.
func TestTheTestCommandAlsoTakesAVariableName(t *testing.T) {
	err := run([]string{"test", "--project", fixture,
		"--dsn-from", "postgres://user:secret@host/db"}, out())
	if err == nil {
		t.Fatal("a DSN was accepted where a variable name belongs")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the refusal printed the password back: %v", err)
	}
}

// `compile` REPORTS the tests without running one, which is what makes it
// the thing to run in a pull request: the count is a fact about the files.
func TestCompileCountsTheTestsWithoutRunningThem(t *testing.T) {
	b := out()
	if err := run([]string{"compile", "--project", "../../internal/check/testdata/project"}, b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "0 function(s)") {
		t.Errorf("compile does not count the functions:\n%s", b)
	}
	if !strings.Contains(b.String(), "4 test(s)") {
		t.Errorf("the fixture declares four tests on one model:\n%s", b)
	}
}

// The `@brevis:` line, which is how a step tells the engine what it wrote.
// One contract for every language, on the pipe that already carries the
// phases -- the Go SDK sends it after a load, a Python step calls
// `landed()`, and this echoes it.
func TestTheLandedLineIsWellFormed(t *testing.T) {
	const marker = "@brevis:"
	for _, line := range []string{
		landedLine("postgres://brevis_it/s/t", nil),
		landedLine("bigquery://p/d/t", ptr(int64(48213))),
	} {
		if !strings.HasPrefix(line, marker) {
			t.Fatalf("no marker: %s", line)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, marker)), &got); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if got["type"] != "landed" {
			t.Errorf(`type is %v, and the engine switches on "landed"`, got["type"])
		}
		if got["target"] == "" || got["target"] == nil {
			t.Errorf("no target: %s", line)
		}
	}
}

// A VIEW SENDS NO `rows` KEY AT ALL, rather than zero. The engine's own
// comment: "Rows and Bytes are pointers because ABSENT IS NOT ZERO. A step
// that does not count says nothing, and a nil summed into a zero would draw
// a table that emptied overnight."
func TestAViewsLandedLineHasNoRowsKey(t *testing.T) {
	line := landedLine("postgres://brevis_it/s/v", nil)
	if strings.Contains(line, "rows") {
		t.Errorf("a view reported a row count: %s", line)
	}

	withRows := landedLine("postgres://brevis_it/s/t", ptr(int64(0)))
	if !strings.Contains(withRows, `"rows":0`) {
		t.Errorf("a table that really is empty must still say 0: %s", withRows)
	}
}

// IT IS ONE LINE. The engine reads the pipe line by line, so a newline
// inside the JSON would split the message into two the marker does not
// start -- and the landing would be dropped without a word.
func TestTheLandedLineIsExactlyOneLine(t *testing.T) {
	line := landedLine("postgres://db/sch/tbl", ptr(int64(7)))
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("the line has a break in it: %q", line)
	}
}

func ptr[T any](v T) *T { return &v }

// ASKING FOR HELP IS NOT A MISTAKE.
//
// `--help` printed the usage and then exited 1 with `"--help" is not a
// command`, because it fell through to the default branch. Every shell
// wrapper, CI step and `make` recipe that asks a binary what it does would
// read that as a failure -- and the gate this commit adds is one of them.
func TestAskingForHelpIsNotAnError(t *testing.T) {
	for _, ask := range []string{"--help", "-h", "help"} {
		b := out()
		if err := run([]string{ask}, b); err != nil {
			t.Errorf("%s: %v", ask, err)
		}
		if !strings.Contains(b.String(), "compile") {
			t.Errorf("%s printed no usage", ask)
		}
	}
	// AND ON STDOUT. Help asked for is an answer; the usage printed beside a
	// refusal is a diagnostic, and only the second belongs on stderr.
	b := out()
	_ = run([]string{"--help"}, b)
	if b.Len() == 0 {
		t.Error("the usage did not reach the writer it was handed")
	}
}

// NO COMMAND IS STILL A MISTAKE, and so is a wrong one.
func TestSayingNothingOrSomethingElseIsStillAnError(t *testing.T) {
	for _, args := range [][]string{{}, {"explain"}} {
		if err := run(args, out()); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

// THE USAGE NAMES EVERY FLAG THE BINARY HAS.
//
// It named seven of eleven. The four it left out were `--max-bytes`,
// `--dry-run`, `--connections` and `--metrics-addr` -- and the first and
// third are the two that bound what `serve` may spend and which warehouses
// it may read at all, which is to say the help was silent about exactly the
// flags somebody running this in front of a warehouse needs.
//
// DERIVED AND NOT LISTED: it walks the FlagSet, so a flag added tomorrow is
// covered the moment it is registered. A list written by hand here would be
// a third place to forget.
func TestTheUsageNamesEveryFlagAndEveryCommand(t *testing.T) {
	b := out()
	usage(b)
	help := b.String()

	fs := flag.NewFlagSet("brevis-sql", flag.ContinueOnError)
	bind(fs)
	fs.VisitAll(func(f *flag.Flag) {
		if !strings.Contains(help, "--"+f.Name) {
			t.Errorf("the usage never mentions --%s", f.Name)
		}
	})
	for _, cmd := range commands {
		if !strings.Contains(help, cmd) {
			t.Errorf("the usage never mentions %q", cmd)
		}
	}
}

// AND EVERY COMMAND THE USAGE NAMES IS ONE THE BINARY HAS, which is the
// other direction and the one that catches a command removed.
func TestEveryCommandTheUsageNamesIsReachable(t *testing.T) {
	for _, cmd := range commands {
		// A wrong dialect is the cheapest proof the command was DISPATCHED:
		// it is checked after the switch and before anything is read, so a
		// command that is not a command fails with a different sentence.
		err := run([]string{cmd, "--dialect", "nope"}, out())
		if err == nil {
			t.Errorf("%s --dialect nope was accepted", cmd)
			continue
		}
		if strings.Contains(err.Error(), "is not a command") {
			t.Errorf("%s is in the usage and is not a command", cmd)
		}
	}
}
