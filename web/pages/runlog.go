package pages

import (
	"strconv"
	"strings"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// logTail is how many of an attempt's last lines the Logs tab draws. A step
// that prints ten megabytes is two hundred thousand lines, and a page that
// draws them all is a tab that hangs; the cause is near the end, and the
// whole log is one click away in the download.
const logTail = 2000

// RunLog is the run's log as the Logs tab draws it: every attempt of every
// step, in the order they ran, numbered through.
type RunLog struct {
	Blocks []LogBlock
	// Lines is how many lines the run's log has, drawn or not.
	Lines int
	// CauseN is the number of the line the run failed on; 0 with none.
	CauseN int
}

// LogBlock is one attempt: its heading, and its lines.
type LogBlock struct {
	Step     string // "load", or "load[2]" for an instance
	Attempt  int    // from 1, as a reader counts
	Status   string
	ExitCode *int
	// Running is a step whose log is not written yet: it arrives when the
	// step ends.
	Running bool
	Items   []LogItem
}

// LogItem is a line, or -- with Gap set -- how many lines were left out.
type LogItem struct {
	N     int
	Text  string
	Level string // "info", "warn" or "err"
	Cause bool
	Gap   int
}

// AttemptText is what an attempt printed, and the engine's own line about
// how it ended when that line is not already the end of the log. The Logs tab
// and the downloaded log.txt read it the same way.
func AttemptText(l postgres.StepLog) string {
	out := strings.TrimSuffix(l.Log, "\n")
	if own := ownLine(l.Err, l.Log); own != "" {
		if out != "" {
			out += "\n"
		}
		out += strings.TrimSuffix(own, "\n")
	}
	return out
}

// ownLine is what the engine's error for a step adds to its log. The error is
// a line and then the log's last lines, for the alert; when those are the end
// of log, only the line is new.
func ownLine(stepErr, log string) string {
	line, tail, ok := strings.Cut(stepErr, "\n")
	if ok && log != "" && strings.HasSuffix(strings.TrimRight(log, "\n"), strings.TrimRight(tail, "\n")) {
		return line
	}
	return stepErr
}

// StepName is a step as a reader names it: an instance carries its index.
func StepName(node string, mapIndex int) string {
	if mapIndex == run.Unmapped {
		return node
	}
	return node + "[" + strconv.Itoa(mapIndex) + "]"
}

// RunLogOf numbers the run's log and reads each line's level. Each attempt
// keeps its last logTail lines, and the line the run failed on wherever it
// is; what is left out is counted, not lost -- the numbers keep counting it.
func RunLogOf(logs []postgres.StepLog, cause *diagnosis.Cause) RunLog {
	var out RunLog
	for _, l := range logs {
		b := LogBlock{Step: StepName(l.NodeID, l.MapIndex), Attempt: l.Attempt + 1,
			Status: l.Status, ExitCode: l.ExitCode}
		text := AttemptText(l)
		if text == "" {
			b.Running = l.Status == "running"
			out.Blocks = append(out.Blocks, b)
			continue
		}
		all := strings.Split(text, "\n")
		causeAt := 0 // 1-based within this attempt
		if cause != nil && cause.LineNo > 0 && cause.Step == l.NodeID &&
			cause.MapIndex == l.MapIndex && cause.Attempt == l.Attempt {
			causeAt = cause.LineNo
		}
		from := len(all) - logTail // the first kept line, 0-based
		gap := 0
		for i, raw := range all {
			n := out.Lines + i + 1
			isCause := i+1 == causeAt
			if i < from && !isCause {
				gap++
				continue
			}
			if gap > 0 {
				b.Items = append(b.Items, LogItem{Gap: gap})
				gap = 0
			}
			line := strings.TrimSuffix(raw, "\r")
			b.Items = append(b.Items, LogItem{N: n, Text: line, Level: diagnosis.LevelOf(line).String(), Cause: isCause})
			if isCause {
				out.CauseN = n
			}
		}
		out.Lines += len(all)
		out.Blocks = append(out.Blocks, b)
	}
	return out
}

// logLevelClass is how a line of each level reads: a warning in the amber the
// queued state already spends, an error on a red ground, so a scroll finds it.
func logLevelClass(level string) string {
	switch level {
	case "warn":
		return "text-warn"
	case "err":
		return "bg-state-failed/10 text-state-failed"
	}
	return "text-ink"
}
