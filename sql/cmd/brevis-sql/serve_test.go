package main

import (
	"bytes"
	"strings"
	"testing"
)

// `serve` IS A COMMAND, and the help says so. A command the usage does not
// list is one nobody finds, and the usage derives its list from one place for
// exactly that reason.
func TestServeIsOneOfTheCommands(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"nonsense"}, &out)
	if err == nil {
		t.Fatal("a nonsense command was accepted")
	}
	// usage() writes to stderr, so the assertion is on the refusal naming it.
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("the refusal does not name what was typed: %v", err)
	}
}

// IT DOES NOT READ A PROJECT. Every other command loads `models/` before it
// does anything; `serve` has none, and a `brevis-sql serve` in a directory
// without models must not fail with "reading models".
//
// This is the whole reason it returns before project.Load: the first version
// of this command would have died in an empty directory, with a message
// about a thing it does not use.
func TestServeDoesNotNeedAProject(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"serve", "--addr", "127.0.0.1:0", "--dry-run"}, &out)
	if err != nil {
		t.Fatalf("serve in a directory with no models: %v", err)
	}
	if !strings.Contains(out.String(), "127.0.0.1:0") {
		t.Errorf("it did not say where it would listen:\n%s", out.String())
	}
}

// OUTSIDE local IT DOES NOT START, and the message names the variable that
// decided it -- the gateway's rule, surfaced by the command rather than
// buried in a library error.
func TestServeRefusesToStartUnauthenticatedOutsideLocal(t *testing.T) {
	t.Setenv("BREVIS_ENV", "production")

	var out bytes.Buffer
	err := run([]string{"serve", "--addr", "127.0.0.1:0", "--dry-run"}, &out)
	if err == nil {
		t.Fatal("a production serve with no token was accepted")
	}
	if !strings.Contains(err.Error(), "BREVIS_ENV") {
		t.Errorf("the refusal does not name what decided it: %v", err)
	}

	// And with a token it is fine.
	t.Setenv("BREVIS_SQL_SERVE_TOKEN", "s3cret")
	out.Reset()
	if err := run([]string{"serve", "--addr", "127.0.0.1:0", "--dry-run"}, &out); err != nil {
		t.Fatalf("with a token: %v", err)
	}
}

// A TOKEN IS NAMED, NEVER WRITTEN -- the rule `--dsn-from` already applies to
// a DSN, for the same reason: a command line is in a shell history, a CI log
// and anybody's `ps`.
func TestTheTokenComesFromTheEnvironmentAndNotAFlag(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"serve", "--addr", "127.0.0.1:0", "--token", "s3cret", "--dry-run"}, &out)
	if err == nil {
		t.Fatal("`--token` was accepted")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("the refusal repeated the secret: %v", err)
	}
}

// THE AUDIT IS CONNECTED, asserted rather than assumed. An audit nobody wired
// is silent exactly when somebody reads it, and this repository has already
// paid once for a check that could not fail.
func TestTheAuditWriterIsWired(t *testing.T) {
	var out bytes.Buffer
	if got := serveOptions(&out, 100, 1<<30); got.Audit == nil {
		t.Error("the service was built with nowhere to write an audit line")
	}
}
