// Package diagnosis reads a run's logs for what went wrong: the line that
// says why a step stopped, the level of every line, and where the first error
// is. Pure functions over text the engine already stored -- computed when a
// screen asks, so a better rule applies to every run ever recorded.
package diagnosis

import (
	"regexp"
	"strings"
)

// Level is how a log line reads: an error, a warning, or neither.
type Level int

const (
	Info Level = iota
	Warn
	Err
)

func (l Level) String() string { return [...]string{"info", "warn", "err"}[l] }

// errorLine is a line that says something failed. Each pattern is here
// because a tool in this ecosystem writes it:
//
//   - a level at the start, any case: Go's `panic:`, git's `fatal:`, dbt's
//     `FAIL 1 not_null_…` and `Failure in test`, rustc's `error[E0308]:`;
//   - an uppercase level after a timestamp, but not as a `KEY=` -- dbt's
//     summary `PASS=4 WARN=0 ERROR=0` is a success;
//   - slog's `level=ERROR`;
//   - Python's and Node's `ValueError:` / `Error:`;
//   - a deadline;
//   - and the frame lines below.
var errorLine = append([]*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*(error|fatal|fail(ed)?|panic|critical)\b`),
	regexp.MustCompile(`(?i)^\s*failure in\b`),
	regexp.MustCompile(`\b(ERROR|FATAL|CRITICAL|PANIC)\b([^=]|$)`),
	regexp.MustCompile(`(?i)\blevel=(error|fatal|critical)\b`),
	regexp.MustCompile(`\b\w*(Error|Exception):`),
	regexp.MustCompile(`(?i)deadline exceeded`),
}, frameLine...)

// frameLine is an error line that frames the story without telling it: a
// non-zero exit, which `go run` prints after every panic, and Python's
// `Traceback`, which opens what the last line finishes. Red on the screen,
// but the cause only when nothing else is.
var frameLine = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bexit (status|code) [1-9]\d*\b`),
	regexp.MustCompile(`^\s*Traceback \(most recent call last\)`),
}

// warnLine is a line that warns without failing: a level at the start or
// after a timestamp, slog's `level=WARN`, and Python's `…Warning:`.
var warnLine = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^\s*warn(ing)?\b`),
	regexp.MustCompile(`\bWARN(ING)?\b([^=]|$)`),
	regexp.MustCompile(`(?i)\blevel=warn(ing)?\b`),
	regexp.MustCompile(`\b\w+Warning:`),
}

// LevelOf reads one line.
//
// A HEURISTIC, and it says so: a word is not a meaning, and a line that
// starts by counting errors reads as one even when the count is zero. The
// screen shows a line among its neighbours, which keeps a false alarm cheap;
// a structured marker from the step is the real answer, and it belongs to
// the steps that can print one.
func LevelOf(line string) Level {
	for _, re := range errorLine {
		if re.MatchString(line) {
			return Err
		}
	}
	for _, re := range warnLine {
		if re.MatchString(line) {
			return Warn
		}
	}
	return Info
}

// FirstError is the line number, counted from 1 like an editor, of the
// first error line in log: where "go to the error" lands when the cause did
// not come from the log itself.
func FirstError(log string) (int, bool) {
	n := 0
	for len(log) > 0 {
		line, rest, _ := strings.Cut(log, "\n")
		n++
		if LevelOf(line) == Err {
			return n, true
		}
		log = rest
	}
	return 0, false
}
