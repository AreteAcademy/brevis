package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/diagnosis"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func lines(items []LogItem) (out []string) {
	for _, it := range items {
		if it.Gap > 0 {
			out = append(out, fmt.Sprintf("…%d", it.Gap))
			continue
		}
		out = append(out, fmt.Sprintf("%d %s %s", it.N, it.Level, it.Text))
	}
	return out
}

// ONE LOG FOR THE RUN, numbered through: each attempt under its own heading,
// in the order they ran, each line with the level it reads as -- the same
// LevelOf the cause was found with, so the two cannot disagree.
func TestTheRunsLogIsOneNumberedLog(t *testing.T) {
	logs := []postgres.StepLog{
		{NodeID: "build", MapIndex: run.Unmapped, Status: "success", Log: "building\ndone\n"},
		{NodeID: "test", MapIndex: run.Unmapped, Attempt: 1, Status: "failed", ExitCode: exit(1),
			Log: "[WARNING]: severity warn\r\nError: 1 test failed\n", Err: `step "test": exited with code 1`},
	}
	got := RunLogOf(logs, nil)
	if len(got.Blocks) != 2 || got.Lines != 5 {
		t.Fatalf("%d blocks, %d lines", len(got.Blocks), got.Lines)
	}
	if b := got.Blocks[1]; b.Step != "test" || b.Attempt != 2 || b.Status != "failed" {
		t.Errorf("the second heading is %+v", b)
	}
	want := []string{"3 warn [WARNING]: severity warn", "4 err Error: 1 test failed", `5 info step "test": exited with code 1`}
	if g := lines(got.Blocks[1].Items); strings.Join(g, "|") != strings.Join(want, "|") {
		t.Errorf("the test attempt reads %q", g)
	}
}

// A step still running has no log yet -- it is written when the step ends --
// and says so rather than looking like a step that printed nothing.
func TestARunningStepSaysItsLogIsComing(t *testing.T) {
	got := RunLogOf([]postgres.StepLog{{NodeID: "load", MapIndex: 2, Status: "running"}}, nil)
	if b := got.Blocks[0]; !b.Running || b.Step != "load[2]" || len(b.Items) != 0 {
		t.Errorf("a running step reads %+v", b)
	}
}

// A HUGE LOG IS ITS END, AND THE CAUSE. Ten megabytes as lines would hang the
// tab, so each attempt keeps its last lines, says how many earlier ones are in
// the download -- and never drops the line the run failed on.
func TestAHugeLogKeepsItsEndAndItsCause(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 5000; i++ {
		if i == 10 {
			b.WriteString("Error: the real cause\n")
			continue
		}
		fmt.Fprintf(&b, "row %d\n", i)
	}
	logs := []postgres.StepLog{{NodeID: "load", MapIndex: run.Unmapped, Status: "failed", Log: b.String()}}
	c := &diagnosis.Cause{Step: "load", MapIndex: run.Unmapped, LineNo: 10}
	got := RunLogOf(logs, c)
	items := got.Blocks[0].Items
	shown := 0
	for _, it := range items {
		if it.Gap == 0 {
			shown++
		}
	}
	if shown != logTail+1 {
		t.Errorf("%d lines kept; wanted the last %d and the cause", shown, logTail)
	}
	if g := lines(items); g[0] != "…9" || g[1] != "10 err Error: the real cause" || g[2] != fmt.Sprintf("…%d", 5000-logTail-10) {
		t.Errorf("the head of the kept lines reads %q", g[:3])
	}
	if got.Lines != 5000 || got.CauseN != 10 || !items[1].Cause {
		t.Errorf("lines %d, cause at %d, marked %v", got.Lines, got.CauseN, items[1].Cause)
	}
	if last := items[len(items)-1]; last.N != 5000 || last.Text != "row 5000" {
		t.Errorf("the log does not end at its end: %+v", last)
	}
}

// The cause names its line within its attempt; the log names it within the
// run. They meet on the step, the instance and the attempt.
func TestTheCauseIsFoundInItsAttempt(t *testing.T) {
	logs := []postgres.StepLog{
		{NodeID: "test", MapIndex: run.Unmapped, Attempt: 0, Status: "failed", Log: "a\nError: first\n"},
		{NodeID: "test", MapIndex: run.Unmapped, Attempt: 1, Status: "failed", Log: "b\nError: second\n"},
	}
	got := RunLogOf(logs, &diagnosis.Cause{Step: "test", MapIndex: run.Unmapped, Attempt: 1, LineNo: 2})
	if got.CauseN != 4 {
		t.Errorf("the cause is line %d of the run; wanted 4", got.CauseN)
	}
}
