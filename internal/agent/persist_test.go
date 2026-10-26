package agent_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// persisted waits for the file a finished execution is written to: it is
// written just after the execution moves to `finished`, which is just after a
// reader saw the end of its stream.
func persisted(t *testing.T, dir, id string) string {
	t.Helper()
	path := filepath.Join(dir, "finished", id+".json")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never written", path)
	return ""
}

// restarted is a new agent on the same state directory, as the process after a
// restart would be.
func restarted(t *testing.T, opt agent.Options) *agent.Agent {
	t.Helper()
	a := agent.New(opt)
	if err := a.Restore(quiet()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return a
}

func resumed(t *testing.T, a *agent.Agent, id string, after int64) []remote.Line {
	t.Helper()
	stream, err := a.Resume(id, after)
	if err != nil {
		t.Fatalf("Resume(%s, %d): %v", id, after, err)
	}
	defer func() { _ = stream.Close() }()
	return lines(t, stream)
}

// The issue (#137): a step ended, nobody read it, the agent restarted. The
// engine coming back must get what the process before would have given it --
// the whole ring, the published context inside it, the exit -- and not
// "unknown execution", which makes it run the step again.
func TestAFinishedExecutionIsResumedAfterARestart(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir}
	before := agent.New(opt)
	want := finish(t, before, "p1",
		`echo one; echo '@brevis:{"type":"context","value":{"from":"before"}}'; exit 3`)
	persisted(t, dir, "p1")

	got := resumed(t, restarted(t, opt), "p1", 0)

	if len(got) != len(want) {
		t.Fatalf("%d lines after the restart, %d before", len(got), len(want))
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Kind != want[i].Kind ||
			got[i].Message != want[i].Message || got[i].Code != want[i].Code {
			t.Errorf("line %d: %+v after the restart, %+v before", i, got[i], want[i])
		}
	}
	if code, ok := exitOf(got); !ok || code != 3 {
		t.Errorf("exit %d (seen %v), wanted 3", code, ok)
	}
	var published bool
	for _, l := range got {
		published = published || strings.Contains(l.Message, `"from":"before"`)
	}
	if !published {
		t.Error("the published context did not survive the restart")
	}
}

// A resume from the middle -- the reattach's fallback to the last line read --
// gets the rest, as it would have before.
func TestAResumeFromTheMiddleSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir}
	want := finish(t, agent.New(opt), "p2", `echo one; echo two; echo three`)
	persisted(t, dir, "p2")

	got := resumed(t, restarted(t, opt), "p2", 2)

	if len(got) != len(want)-2 || got[0].Seq != 3 {
		t.Fatalf("from after 2: %d lines starting at %d, wanted %d starting at 3",
			len(got), got[0].Seq, len(want)-2)
	}
}

// A ring that had moved on before the restart has moved on after it: the gap
// is still answered as a gap, never served with a hole.
func TestAGapIsStillAGapAfterARestart(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir, RingSize: 3}
	_ = finish(t, agent.New(opt), "p3", `for i in 1 2 3 4 5 6; do echo $i; done`)
	persisted(t, dir, "p3")

	_, err := restarted(t, opt).Resume("p3", 0)
	var gap remote.GapError
	if !errors.As(err, &gap) {
		t.Fatalf("Resume from 0 over a trimmed ring = %v, wanted a gap", err)
	}
}

// The ring holds what the step printed, which may be anything; it is kept the
// way the pid record is, and written whole or not at all.
func TestTheFileIsPrivateAndWrittenWhole(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir}
	_ = finish(t, agent.New(opt), "p4", `echo secret-ish`)
	path := persisted(t, dir, "p4")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the file is %o, wanted 600", perm)
	}
	dinfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dinfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("the directory is %o, wanted 700", perm)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "finished", "*.tmp"))
	if len(leftovers) > 0 {
		t.Errorf("a temporary file was left behind: %v", leftovers)
	}
}

// Without a state directory nothing changes: a restart forgets, as it did.
func TestWithNoStateDirARestartStillForgets(t *testing.T) {
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}}
	_ = finish(t, agent.New(opt), "p5", `echo gone`)

	_, err := restarted(t, opt).Resume("p5", 0)
	if !errors.Is(err, remote.ErrUnknownExecution) {
		t.Errorf("Resume after a restart with no state = %v, wanted unknown execution", err)
	}
}

// Cancelling what was restored is the honest no-op cancelling a finished one
// always was: what was asked for is already true.
func TestCancellingARestoredExecutionIsANoOp(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir}
	_ = finish(t, agent.New(opt), "p6", `true`)
	persisted(t, dir, "p6")

	if err := restarted(t, opt).Cancel("p6"); err != nil {
		t.Errorf("Cancel of a restored execution: %v", err)
	}
}
