package agent_test

import (
	"context"
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

// gone says the file of a finished execution is no longer on disk, waiting a
// little: removing it is part of the same step as dropping it from memory, but
// the step may be one this test only triggers.
func gone(t *testing.T, dir, id string) bool {
	t.Helper()
	path := filepath.Join(dir, "finished", id+".json")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// The disk follows memory: what --keep-finished lets go is let go on disk too,
// or a restart would answer for an execution the process before had forgotten.
func TestPastItsRetentionTheFileIsRemoved(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir,
		KeepFinished: time.Hour, Now: c.Now})
	_ = finish(t, a, "aged", "true")
	persisted(t, dir, "aged")

	c.advance(61 * time.Minute)
	_, _ = a.Resume("aged", 0) // a lookup is what prunes

	if !gone(t, dir, "aged") {
		t.Error("the execution left memory past its retention, and its file stayed")
	}
}

// And past --keep-finished-max, the oldest file goes with the oldest entry.
func TestPastTheMaximumTheOldestFileIsRemoved(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir,
		KeepFinished: time.Hour, KeepFinishedMax: 2, Now: c.Now})
	for _, id := range []string{"oldest", "middle", "newest"} {
		_ = finish(t, a, id, "true")
		persisted(t, dir, id)
		c.advance(time.Minute)
	}

	if !gone(t, dir, "oldest") {
		t.Error("the oldest of three left memory under a maximum of two, and its file stayed")
	}
	for _, id := range []string{"middle", "newest"} {
		if gone(t, dir, id) {
			t.Errorf("%s was kept in memory and its file was removed", id)
		}
	}
}

// A start under the id of a finished execution replaces it (an engine before
// #127 reuses ids). The replaced one is gone from disk at once, so a restart
// while the new one runs cannot answer with the old outcome.
func TestAStartOfAFinishedIdRemovesItsFile(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir}
	a := agent.New(opt)
	_ = finish(t, a, "again", "echo old")
	persisted(t, dir, "again")

	stream, err := a.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "again", NodeID: "step", Command: "sleep 5",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Stopped and read to its end, its file waited for: written after the
		// test returned, it would land in a directory being removed.
		_ = a.Cancel("again")
		_ = lines(t, stream)
		_ = stream.Close()
		persisted(t, dir, "again")
	})

	if !gone(t, dir, "again") {
		t.Fatal("the replaced execution is still on disk")
	}
	if _, err := restarted(t, opt).Resume("again", 0); !errors.Is(err, remote.ErrUnknownExecution) {
		t.Errorf("a restart while the new one runs answered %v; wanted unknown execution", err)
	}
}

// The write of an execution that was dropped meanwhile is thrown away: it
// happens outside the lock, and arriving late must not bring it back.
func TestALateWriteDoesNotBringBackWhatWasDropped(t *testing.T) {
	dir := t.TempDir()
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir})

	a.PersistDropped("late")

	if _, err := os.Stat(filepath.Join(dir, "finished", "late.json")); err == nil {
		t.Error("an execution no longer kept was written")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "finished", "*.tmp"))
	if len(leftovers) > 0 {
		t.Errorf("the thrown-away write left %v", leftovers)
	}
}

// Retention holds across a restart: --keep-finished counts from when the
// execution finished, not from when the agent came back.
func TestARestartForgetsWhatOutlivedItsRetention(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir,
		KeepFinished: time.Hour, Now: c.Now}
	before := agent.New(opt)
	_ = finish(t, before, "old", "true")
	persisted(t, dir, "old")
	c.advance(50 * time.Minute)
	_ = finish(t, before, "recent", "true")
	persisted(t, dir, "recent")

	c.advance(20 * time.Minute) // old is 70 minutes over, recent 20
	after := restarted(t, opt)

	if _, err := after.Resume("old", 0); err == nil {
		t.Error("an execution past its retention was restored")
	}
	if !gone(t, dir, "old") {
		t.Error("an execution past its retention was left on disk")
	}
	r, err := after.Resume("recent", 0)
	if err != nil {
		t.Fatalf("an execution within its retention was not restored: %v", err)
	}
	_ = r.Close()
}

// And --keep-finished-max: a restart keeps the newest, even when the agent
// before it kept more under a larger maximum.
func TestARestartKeepsTheNewestUpToTheMaximum(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir,
		KeepFinished: time.Hour, KeepFinishedMax: 3, Now: c.Now}
	before := agent.New(opt)
	for _, id := range []string{"first", "second", "third"} {
		_ = finish(t, before, id, "true")
		persisted(t, dir, id)
		c.advance(time.Minute)
	}

	opt.KeepFinishedMax = 2
	after := restarted(t, opt)

	if _, err := after.Resume("first", 0); err == nil {
		t.Error("the oldest was restored past a maximum of two")
	}
	if !gone(t, dir, "first") {
		t.Error("the oldest was left on disk past a maximum of two")
	}
	for _, id := range []string{"second", "third"} {
		r, err := after.Resume(id, 0)
		if err != nil {
			t.Errorf("%s was not restored: %v", id, err)
			continue
		}
		_ = r.Close()
	}
}

// A file that is wrong -- cut short by a full disk, or not JSON at all -- is
// said and removed, and the agent comes up with what it could read.
func TestARottenFileIsRemovedAndTheAgentStillStarts(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir}
	_ = finish(t, agent.New(opt), "good", "echo fine")
	good := persisted(t, dir, "good")
	raw, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	rotten := map[string][]byte{"truncated": raw[:len(raw)/2], "garbage": []byte("not json\n")}
	for id, content := range rotten {
		if err := os.WriteFile(filepath.Join(dir, "finished", id+".json"), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	after := restarted(t, opt)

	for id := range rotten {
		if _, err := os.Stat(filepath.Join(dir, "finished", id+".json")); err == nil {
			t.Errorf("the %s file was left in place", id)
		}
		if _, err := after.Resume(id, 0); err == nil {
			t.Errorf("the %s file was restored", id)
		}
	}
	r, err := after.Resume("good", 0)
	if err != nil {
		t.Fatalf("a rotten file beside it cost the good one: %v", err)
	}
	_ = r.Close()
}

// The pid record (<dir>/<id>.json) is read by a cancel across a restart in a
// format that must not change. The finished executions live beside it, in
// their own directory, and a restart that restores them still cancels by it.
func TestThePidRecordStillCancelsAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	opt := agent.Options{Shell: []string{"/bin/sh", "-c"}, StateDir: dir}
	before := agent.New(opt)
	_ = finish(t, before, "done", "true")
	persisted(t, dir, "done")

	stream, err := before.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "running", NodeID: "step", Command: "sleep 30",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	record := filepath.Join(dir, "running.json")
	deadline := time.Now().Add(3 * time.Second)
	for _, err := os.Stat(record); err != nil; _, err = os.Stat(record) {
		if time.Now().After(deadline) {
			t.Fatalf("the pid record %s was never written", record)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := restarted(t, opt).Cancel("running"); err != nil {
		t.Fatalf("Cancel across a restart: %v", err)
	}

	ended := make(chan []remote.Line, 1)
	go func() { ended <- lines(t, stream) }()
	select {
	case got := <-ended:
		if code, ok := exitOf(got); !ok || code == 0 {
			t.Errorf("exit %d (seen %v): the cancel did not stop the step", code, ok)
		}
		// Its end is written last; waited for, so it does not land in a
		// directory the test is already removing.
		persisted(t, dir, "running")
	case <-time.After(5 * time.Second):
		t.Fatal("the step kept running: the pid record was not read")
	}
}
