package diagnosis

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)

func failed(step string, at time.Duration, log string) StepAttempt {
	return StepAttempt{Step: step, MapIndex: -1, Attempt: 1, Status: "failed", Log: log, StartedAt: t0.Add(at)}
}

func exit(n int) *int { return &n }

// THE CAUSE IS THE LAST ERROR LINE: every tool here writes its conclusion at
// the end, after the stack that led to it. Each log is what the tool prints.
func TestTheCauseIsTheLastErrorLineOfTheFailedStep(t *testing.T) {
	for name, c := range map[string]struct {
		log  string
		line string
		no   int
	}{
		"go panic": {
			"starting\npanic: runtime error: index out of range [3] with length 3\n\ngoroutine 1 [running]:\nmain.main()\n\t/app/main.go:12 +0x1d\nexit status 2\n",
			"panic: runtime error: index out of range [3] with length 3", 2,
		},
		"python traceback": {
			"loading orders\nTraceback (most recent call last):\n  File \"/app/load.py\", line 9, in <module>\n    int(x)\nValueError: invalid literal for int() with base 10: 'x'\n",
			"ValueError: invalid literal for int() with base 10: 'x'", 5,
		},
		"node": {
			"> node index.js\nconnecting\nError: connect ECONNREFUSED 127.0.0.1:5432\n    at TCPConnectWrap.afterConnect [as oncomplete] (node:net:1555:16)\n",
			"Error: connect ECONNREFUSED 127.0.0.1:5432", 3,
		},
		"dbt": {
			"Running with dbt=1.8.0\n1 of 2 PASS unique_alerts_id\n2 of 2 FAIL 1 not_null_alerts_id ... [FAIL 1 in 0.1s]\nFailure in test not_null_alerts_id (models/schema.yml)\n  Got 1 result, configured to fail if != 0\nDone. PASS=1 WARN=0 ERROR=1 SKIP=0 TOTAL=2\n",
			"Failure in test not_null_alerts_id (models/schema.yml)", 4,
		},
		"deadline": {
			"fetching page 1\nfetching page 2\nGet \"https://api.example.com/x\": context deadline exceeded\n",
			"Get \"https://api.example.com/x\": context deadline exceeded", 3,
		},
	} {
		got := Why([]StepAttempt{failed("load", 0, c.log)}, "step load failed")
		if got == nil || got.Line != c.line || got.LineNo != c.no || got.Source != FromLog || got.Step != "load" {
			t.Errorf("%s: Why = %+v; want line %d %q from the log", name, got, c.no, c.line)
		}
	}
}

// A TRACEBACK OR AN EXIT STATUS FRAMES THE STORY, IT DOES NOT TELL IT: `go
// run` ends every panic with `exit status 2`, and the exit code is shown on
// its own. They count only when nothing else in the log reads as an error.
func TestATracebackOrAnExitStatusIsTheCauseOnlyWhenNothingElseIs(t *testing.T) {
	log := "loading\nTraceback (most recent call last):\n  File \"/app/load.py\", line 9\n"
	if got := Why([]StepAttempt{failed("load", 0, log)}, ""); got == nil || got.LineNo != 2 {
		t.Errorf("Why = %+v; want the Traceback on line 2", got)
	}
	log = "loading\nexit status 1\nbye\n"
	if got := Why([]StepAttempt{failed("load", 0, log)}, ""); got == nil || got.LineNo != 2 {
		t.Errorf("Why = %+v; want the exit status on line 2", got)
	}
}

// NO ERROR LINE AT ALL: the last thing the step said is still the best
// guess, and better than the engine's generic message.
func TestWithoutAnErrorLineTheLastNonEmptyLineIsTheCause(t *testing.T) {
	got := Why([]StepAttempt{failed("load", 0, "fetched 3 rows\nwriting\n\n  \n")}, "")
	if got == nil || got.Line != "writing" || got.LineNo != 2 || got.Source != FromLog {
		t.Errorf("Why = %+v; want line 2 %q", got, "writing")
	}
}

// AN EMPTY LOG: the step never got to speak (an image that did not pull, a
// timeout before the first line), so the engine's error is the cause.
func TestAnEmptyLogFallsBackToTheStepsError(t *testing.T) {
	a := failed("load", 0, "")
	a.Err = `pulling image "ghcr.io/x/load:1": not found`
	got := Why([]StepAttempt{a}, "step load failed")
	if got == nil || got.Line != a.Err || got.Source != FromStepError || got.LineNo != 0 || got.Step != "load" {
		t.Errorf("Why = %+v; want the step's error", got)
	}
}

// NO STEP AT ALL: the run died before any step started, and only the run
// knows why.
func TestARunWithoutStepsFallsBackToTheRunsError(t *testing.T) {
	got := Why(nil, "workflow daily_sales has no version 7")
	if got == nil || got.Line != "workflow daily_sales has no version 7" || got.Source != FromRunError || got.Step != "" {
		t.Errorf("Why = %+v; want the run's error", got)
	}
	if got := Why(nil, ""); got != nil {
		t.Errorf("Why with nothing to say = %+v; want nil", got)
	}
}

// TWO STEPS FAILED: the run stopped at the one that failed last, the same
// rule as the Slack alert -- started later, then the higher attempt.
func TestTheMostRecentFailureIsTheCause(t *testing.T) {
	first := failed("extract", 0, "Error: first")
	last := failed("load", time.Minute, "Error: last")
	last.ExitCode = exit(3)
	got := Why([]StepAttempt{last, first}, "")
	if got == nil || got.Step != "load" || got.Line != "Error: last" || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Errorf("Why = %+v; want load's error with exit 3", got)
	}

	retry := failed("load", time.Minute, "Error: second attempt")
	retry.Attempt = 2
	got = Why([]StepAttempt{retry, last}, "")
	if got == nil || got.Attempt != 2 || got.Line != "Error: second attempt" {
		t.Errorf("Why with the same start = %+v; want attempt 2", got)
	}
}

// A STEP THAT PASSED ON RETRY did not stop the run, whatever its first
// attempt said.
func TestAStepThatPassedOnRetryIsNotTheCause(t *testing.T) {
	flaky := failed("extract", time.Minute, "Error: flaky")
	passed := StepAttempt{Step: "extract", MapIndex: -1, Attempt: 2, Status: "success", StartedAt: t0.Add(2 * time.Minute)}
	broken := failed("load", 0, "Error: the real one")
	if got := Why([]StepAttempt{flaky, passed, broken}, ""); got == nil || got.Step != "load" {
		t.Errorf("Why = %+v; want load", got)
	}
	if got := Why([]StepAttempt{flaky, passed}, "canceled by the deadline"); got == nil || got.Source != FromRunError {
		t.Errorf("Why with only a recovered step = %+v; want the run's error", got)
	}
}

// THE EXCERPT is what the screen shows around the cause: up to five lines
// before it, and the cause itself.
func TestTheExcerptIsUpToFiveLinesBeforeTheCause(t *testing.T) {
	log := "1\n2\n3\n4\n5\n6\n7\nError: 8\n9\n"
	got := Why([]StepAttempt{failed("load", 0, log)}, "")
	want := []string{"3", "4", "5", "6", "7", "Error: 8"}
	if got == nil || strings.Join(got.Excerpt, ",") != strings.Join(want, ",") {
		t.Errorf("Excerpt = %q; want %q", got.Excerpt, want)
	}

	got = Why([]StepAttempt{failed("load", 0, "a\r\nError: b\r\n")}, "")
	if got == nil || strings.Join(got.Excerpt, ",") != "a,Error: b" || got.LineNo != 2 {
		t.Errorf("Excerpt near the top = %+v; want [a Error: b] at line 2", got)
	}
}

// A 10 MB LOG IS READ FROM THE END, and nothing of it is copied: the cause
// sits near the end, and a screen should not pay for the whole log to show
// one line.
func TestALargeLogIsNotCopied(t *testing.T) {
	log := strings.Repeat("fetched a page of rows from the upstream api\n", 10<<20/45) + "Error: boom\n"
	steps := []StepAttempt{failed("load", 0, log)}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := Why(steps, "")
	runtime.ReadMemStats(&after)

	if got == nil || got.Line != "Error: boom" {
		t.Fatalf("Why = %+v", got)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 64<<10 {
		t.Errorf("Why allocated %d bytes for a %d-byte log; want it read in place", n, len(log))
	}
}
