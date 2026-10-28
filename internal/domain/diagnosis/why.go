package diagnosis

import (
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/run"
)

// StepAttempt is one attempt of one step, as the engine stored it.
type StepAttempt struct {
	Step string
	// MapIndex is the instance of a mapped step, run.Unmapped for the rest.
	MapIndex  int
	Attempt   int
	Status    string
	ExitCode  *int
	Err       string
	Log       string
	StartedAt time.Time
}

// Source says where a cause was read: the step's log, the engine's error for
// the step, or the run's own error.
type Source string

const (
	FromLog       Source = "log"
	FromStepError Source = "step_error"
	FromRunError  Source = "run_error"
)

// excerptBefore is how many lines before the cause the screen shows with it.
const excerptBefore = 5

// Cause is why a run failed.
type Cause struct {
	// Step, MapIndex and Attempt name the attempt that stopped the run;
	// Step is empty when the run died before any step started.
	Step     string
	MapIndex int
	Attempt  int
	ExitCode *int
	// Line is the line that says why; LineNo counts from 1 in that attempt's
	// log, and is 0 when the line did not come from the log.
	Line   string
	LineNo int
	Source Source
	// Excerpt is up to five lines before the cause, and the cause itself.
	Excerpt []string
}

// Why reads why a run failed: the attempt that failed last and is not
// redeemed by a later success of the same step -- the same rule the alert
// uses, started last, then the higher attempt -- and in its log the last
// line that reads as an error.
//
// When the log has no error line, the last thing it said; when it said
// nothing, the engine's error for the step; with no failed step at all, the
// run's error. Nil when there is nothing to say.
func Why(steps []StepAttempt, runErr string) *Cause {
	var at *StepAttempt
	for i := range steps {
		s := &steps[i]
		if s.Status != string(run.StatusFailed) || redeemed(steps, s) {
			continue
		}
		if at == nil || s.StartedAt.After(at.StartedAt) ||
			s.StartedAt.Equal(at.StartedAt) && s.Attempt > at.Attempt {
			at = s
		}
	}
	if at == nil {
		if runErr == "" {
			return nil
		}
		return &Cause{MapIndex: run.Unmapped, Line: runErr, Source: FromRunError}
	}

	c := &Cause{Step: at.Step, MapIndex: at.MapIndex, Attempt: at.Attempt, ExitCode: at.ExitCode}
	switch start, ok := causeLine(at.Log); {
	case ok:
		c.Source = FromLog
		c.LineNo = strings.Count(at.Log[:start], "\n") + 1
		c.Excerpt = excerpt(at.Log, start)
		c.Line = c.Excerpt[len(c.Excerpt)-1]
	case at.Err != "":
		c.Line, c.Source = at.Err, FromStepError
	default:
		c.Line, c.Source = runErr, FromRunError
	}
	return c
}

// redeemed is a failed attempt whose step later passed: a retry that worked
// did not stop the run.
func redeemed(steps []StepAttempt, s *StepAttempt) bool {
	for _, o := range steps {
		if o.Step == s.Step && o.MapIndex == s.MapIndex && o.Attempt > s.Attempt &&
			o.Status == string(run.StatusSuccess) {
			return true
		}
	}
	return false
}

// causeLine is the offset where the cause starts in log: the last error line
// that is not a frame, else the last frame, else the last non-empty line.
//
// Read from the end, in place: the conclusion is near the end, and a 10 MB
// log should not be split or copied to find one line.
func causeLine(log string) (int, bool) {
	frame, said := -1, -1
	for rest := log; ; {
		i := strings.LastIndexByte(rest, '\n')
		line := strings.TrimSuffix(rest[i+1:], "\r")
		if strings.TrimSpace(line) != "" {
			if said < 0 {
				said = i + 1
			}
			if LevelOf(line) == Err {
				if !isFrame(line) {
					return i + 1, true
				}
				if frame < 0 {
					frame = i + 1
				}
			}
		}
		if i < 0 {
			break
		}
		rest = rest[:i]
	}
	if frame >= 0 {
		return frame, true
	}
	return said, said >= 0
}

// isFrame is a line that only frame patterns call an error.
func isFrame(line string) bool {
	for _, re := range errorLine[:len(errorLine)-len(frameLine)] {
		if re.MatchString(line) {
			return false
		}
	}
	return true
}

// excerpt is the cause's line, starting at start, with up to excerptBefore
// lines before it -- copied, so the screen does not keep the whole log alive.
func excerpt(log string, start int) []string {
	end := strings.IndexByte(log[start:], '\n')
	if end < 0 {
		end = len(log) - start
	}
	lines := []string{strings.Clone(strings.TrimSuffix(log[start:start+end], "\r"))}
	for rest := log[:start]; len(lines) <= excerptBefore && rest != ""; {
		rest = rest[:len(rest)-1] // the newline that ended the line before
		i := strings.LastIndexByte(rest, '\n')
		lines = append(lines, strings.Clone(strings.TrimSuffix(rest[i+1:], "\r")))
		rest = rest[:i+1]
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}
