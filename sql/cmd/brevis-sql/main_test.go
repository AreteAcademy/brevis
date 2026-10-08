package main

import (
	"bytes"
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
	os.Unsetenv(name)
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
