package execution

import (
	"fmt"
	"strings"
)

// LogCeiling is how much of a step's output goes to the database.
//
// 128 KB comfortably covers a dbt run with 60 nodes (~25 KB of text) and still
// holds a noisy backfill. The ceiling exists because a `while true; do echo` in
// any workflow must not be able to fill Postgres's disk.
const LogCeiling = 128 << 10

// startSlice is how much of the ceiling is reserved for the START of the
// output when it does not fit whole. Only the end would be simpler, but it would
// lose the command that ran and the configuration it printed at the outset —
// half the diagnosis.
const startSlice = LogCeiling / 4

// janela accumulates a step's output within the ceiling, keeping the start and
// the end and saying how much it discarded in the middle.
//
// The alternative — cutting when it overflows and keeping only the start — loses
// exactly the stretch where a program reports why it failed.
type window struct {
	start  strings.Builder
	end    []string // a ring buffer of the recent lines
	endLen int      // live bytes in `end`
	cut    int      // bytes discarded from the middle
}

// Write appends one line.
func (j *window) Write(line string) {
	n := len(line) + 1 // +1 for the newline

	if j.start.Len()+n <= startSlice {
		j.start.WriteString(line)
		j.start.WriteByte('\n')
		return
	}

	j.end = append(j.end, line)
	j.endLen += n

	// It discards from the front until it is back under the ceiling. What leaves
	// here already went past the start, so it really is the middle — the least
	// useful part of the two ends.
	for j.endLen > LogCeiling-startSlice && len(j.end) > 1 {
		j.cut += len(j.end[0]) + 1
		j.endLen -= len(j.end[0]) + 1
		j.end = j.end[1:]
	}
}

// String assembles the final text, with a marker for what was left out.
//
// Every line comes out newline-terminated, the last one included: a log where
// the first lines end in \n and the last ones do not is uncomfortable to read and
// sets a trap for whoever concatenates it later.
func (j *window) String() string {
	if j.cut == 0 {
		return j.start.String() + lines(j.end)
	}
	// The marker is explicit: a log truncated in silence makes the reader
	// conclude the program stopped there.
	var b strings.Builder
	b.WriteString(j.start.String())
	fmt.Fprintf(&b, "\n[... %s omitted by the %s per-step limit ...]\n\n",
		inKB(j.cut), inKB(LogCeiling))
	b.WriteString(lines(j.end))
	return b.String()
}

// lines joins them terminating each with a newline. Empty stays empty — it does
// not produce a stray newline.
func lines(ls []string) string {
	if len(ls) == 0 {
		return ""
	}
	return strings.Join(ls, "\n") + "\n"
}

func inKB(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%d KB", n/1024)
}
