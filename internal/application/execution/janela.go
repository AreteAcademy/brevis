package execution

import (
	"fmt"
	"strings"
)

// TetoDoLog is how much of a step's output goes to the database.
//
// 128 KB comfortably covers a dbt run with 60 nodes (~25 KB of text) and still
// holds a noisy backfill. The ceiling exists because a `while true; do echo` in
// any workflow must not be able to fill Postgres's disk.
const TetoDoLog = 128 << 10

// fatiaDoInicio is how much of the ceiling is reserved for the START of the
// output when it does not fit whole. Only the end would be simpler, but it would
// lose the command that ran and the configuration it printed at the outset —
// half the diagnosis.
const fatiaDoInicio = TetoDoLog / 4

// janela accumulates a step's output within the ceiling, keeping the start and
// the end and saying how much it discarded in the middle.
//
// The alternative — cutting when it overflows and keeping only the start — loses
// exactly the stretch where a program reports why it failed.
type janela struct {
	inicio  strings.Builder
	fim     []string // a ring buffer of the recent lines
	fimLen  int      // bytes vivos em `fim`
	cortado int      // bytes descartados no meio
}

// Escrever appends one line.
func (j *janela) Escrever(linha string) {
	n := len(linha) + 1 // +1 for the newline

	if j.inicio.Len()+n <= fatiaDoInicio {
		j.inicio.WriteString(linha)
		j.inicio.WriteByte('\n')
		return
	}

	j.fim = append(j.fim, linha)
	j.fimLen += n

	// It discards from the front until it is back under the ceiling. What leaves
	// here already went past the start, so it really is the middle — the least
	// useful part of the two ends.
	for j.fimLen > TetoDoLog-fatiaDoInicio && len(j.fim) > 1 {
		j.cortado += len(j.fim[0]) + 1
		j.fimLen -= len(j.fim[0]) + 1
		j.fim = j.fim[1:]
	}
}

// String assembles the final text, with a marker for what was left out.
//
// Every line comes out newline-terminated, the last one included: a log where
// the first lines end in \n and the last ones do not is uncomfortable to read and
// sets a trap for whoever concatenates it later.
func (j *janela) String() string {
	if j.cortado == 0 {
		return j.inicio.String() + linhas(j.fim)
	}
	// The marker is explicit: a log truncated in silence makes the reader
	// conclude the program stopped there.
	var b strings.Builder
	b.WriteString(j.inicio.String())
	fmt.Fprintf(&b, "\n[... %s omitted by the %s per-step limit ...]\n\n",
		emKB(j.cortado), emKB(TetoDoLog))
	b.WriteString(linhas(j.fim))
	return b.String()
}

// linhas joins them terminating each with a newline. Empty stays empty — it does
// not produce a stray newline.
func linhas(ls []string) string {
	if len(ls) == 0 {
		return ""
	}
	return strings.Join(ls, "\n") + "\n"
}

func emKB(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%d KB", n/1024)
}
