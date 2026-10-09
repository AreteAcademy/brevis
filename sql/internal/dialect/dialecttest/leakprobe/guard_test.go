package main

import (
	"context"
	"strings"
	"testing"
)

// THE GUARD IS THE ONLY DANGEROUS LINE IN THIS PROGRAM, so it is the one with
// a test. `drop` issues `DROP SCHEMA ... CASCADE` against a project holding a
// client's real medallion layers, and the single thing standing between it and
// them is a prefix check.
func TestDropRefusesAnythingItDidNotMake(t *testing.T) {
	for _, name := range []string{
		"bronze", "silver", "gold", "staging", "analysis", "audit", "geography",
		"brevis_ci_sandbox",
		"bvs_run_it",   // a leftover, and still not this script's to take
		"bvs_conf_123", // the conformance suite's, which cleans up its own
		"bvs_inc_123",
		"", "bvs_kil", "Bvs_kill_1", " bvs_kill_1",
	} {
		t.Run(name, func(t *testing.T) {
			err := drop(context.Background(), refusingConn{t}, name)
			if err == nil {
				t.Fatalf("it would have dropped %q", name)
			}
			if !strings.Contains(err.Error(), "only what it made") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// And it does accept its own, which is what keeps the guard from being a
// check that refuses everything and passes the test above by accident.
func TestDropAcceptsWhatItMade(t *testing.T) {
	spy := &recordingConn{}
	if err := drop(context.Background(), spy, "bvs_kill_1791509820_73675"); err != nil {
		t.Fatal(err)
	}
	if len(spy.ran) != 1 || !strings.HasPrefix(spy.ran[0], "DROP SCHEMA IF EXISTS bvs_kill_") {
		t.Errorf("ran %v", spy.ran)
	}
}

// refusingConn fails the test if anything reaches the warehouse at all.
type refusingConn struct{ t *testing.T }

func (c refusingConn) Exec(_ context.Context, s string) error {
	c.t.Fatalf("it reached the warehouse before refusing:\n%s", s)
	return nil
}
func (c refusingConn) Scalar(context.Context, string) (any, error) { return nil, nil }
func (c refusingConn) Target(ref string) string                    { return ref }
func (c refusingConn) Close(context.Context) error                 { return nil }

type recordingConn struct{ ran []string }

func (c *recordingConn) Exec(_ context.Context, s string) error { c.ran = append(c.ran, s); return nil }
func (c *recordingConn) Scalar(context.Context, string) (any, error) {
	return nil, nil
}
func (c *recordingConn) Target(ref string) string    { return ref }
func (c *recordingConn) Close(context.Context) error { return nil }
