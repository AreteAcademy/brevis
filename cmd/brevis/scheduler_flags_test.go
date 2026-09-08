package main

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/scheduler"
	"github.com/spf13/cobra"
)

// TestTheRetryFlagsReachTheDispatcher.
//
// A flag that parses into a variable nobody passes on is a flag that does
// nothing, and it looks exactly like a working one: `--help` lists it, the
// value parses, and the behaviour never changes. This repository has shipped
// that twice -- the Kubernetes return path and Runner.ContextDir -- both green,
// both inert, and both found by a question rather than by a test.
//
// So the wiring is asserted end to end: parse the flag, read the Config the
// dispatcher is actually built from.
func TestTheRetryFlagsReachTheDispatcher(t *testing.T) {
	var f schedulerFlags
	c := &cobra.Command{Use: "scheduler"}
	f.bind(c)

	if err := c.ParseFlags([]string{
		"--max-attempts", "5",
		"--retry-backoff", "2m",
		"--retry-backoff-max", "10m",
		"--concurrency", "7",
	}); err != nil {
		t.Fatal(err)
	}

	cfg := f.dispatcher()
	if cfg.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, the flag said 5", cfg.MaxAttempts)
	}
	if cfg.BackoffBase != 2*time.Minute {
		t.Errorf("BackoffBase = %s, the flag said 2m", cfg.BackoffBase)
	}
	if cfg.BackoffMax != 10*time.Minute {
		t.Errorf("BackoffMax = %s, the flag said 10m", cfg.BackoffMax)
	}
	if cfg.MaxConcorrente != 7 {
		t.Errorf("MaxConcorrente = %d", cfg.MaxConcorrente)
	}
}

// TestTheFlagDefaultsAreTheDispatchersDefaults.
//
// The numbers are written once, in the dispatcher, and `scheduler --help`
// reads them from there. If somebody ever types them here instead, this fails
// the day the two disagree -- which is the same class as the node type the API
// and the island disagreed about, and that one shipped for two weeks drawing
// nothing.
func TestTheFlagDefaultsAreTheDispatchersDefaults(t *testing.T) {
	var f schedulerFlags
	c := &cobra.Command{Use: "scheduler"}
	f.bind(c)
	if err := c.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}

	policy := scheduler.Defaults()
	cfg := f.dispatcher()
	if cfg.MaxAttempts != policy.MaxAttempts {
		t.Errorf("--max-attempts defaults to %d, the dispatcher to %d",
			cfg.MaxAttempts, policy.MaxAttempts)
	}
	if cfg.BackoffBase != policy.BackoffBase {
		t.Errorf("--retry-backoff defaults to %s, the dispatcher to %s",
			cfg.BackoffBase, policy.BackoffBase)
	}
	if cfg.BackoffMax != policy.BackoffMax {
		t.Errorf("--retry-backoff-max defaults to %s, the dispatcher to %s",
			cfg.BackoffMax, policy.BackoffMax)
	}
}

// TestTheBootLineSaysWhenTheAttemptsLand.
//
// Until this change, the only way to find out that a run's three attempts
// landed at 0s, 1s and 3s was to read Go. Two numbers in the log are two
// numbers an operator then has to compose; the times themselves are the answer
// they were composing them for.
func TestTheBootLineSaysWhenTheAttemptsLand(t *testing.T) {
	for _, c := range []struct {
		attempts  int
		base, max time.Duration
		want      string
	}{
		// The default policy, which is the line most people will ever see.
		{3, 30 * time.Second, time.Hour, "0s, 30s, 1m30s"},
		// What the consumer who reported this asks for.
		{3, time.Minute, time.Hour, "0s, 1m0s, 3m0s"},
		// The old default, for comparison: three attempts inside three seconds.
		{3, time.Second, time.Hour, "0s, 1s, 3s"},
		// The cap binding.
		{5, time.Minute, 2 * time.Minute, "0s, 1m0s, 3m0s, 5m0s, 7m0s"},
		// One attempt is no retry, and saying "0s" would read as one.
		{1, time.Minute, time.Hour, "no retry"},
	} {
		if got := retrySchedule(c.attempts, c.base, c.max); got != c.want {
			t.Errorf("%d attempts base %s: %q, wanted %q", c.attempts, c.base, got, c.want)
		}
	}
}
