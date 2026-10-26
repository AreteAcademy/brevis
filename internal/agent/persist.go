package agent

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// A finished execution outlives the process that ran it (#137).
//
// The agent keeps a finished execution for --keep-finished so an engine that
// was away while it ended can still read how it ended. In memory only, a
// restart lost it, and the engine coming back was answered "unknown execution"
// -- which, since #125, starts the step AGAIN under the same id. A load that
// finished ran twice.
//
// So with --state-dir a finished execution is written when it finishes and
// read back when the agent starts, and a resume after a restart is answered as
// the process before would have answered it. THE WHOLE RING, not the exit
// alone: the published context travels inside it as marker lines, and a
// reattaching scheduler reads it from the start, or from the last line it had.
//
// What must never happen: a file that is wrong taking the agent down, or a
// half-written one being read as an outcome. A file is written whole or not at
// all, and one that cannot be read is said and removed.

// finishedRecord is what is written. The lines are the protocol's own JSON.
type finishedRecord struct {
	FinishedAt time.Time     `json:"finished_at"`
	First      int64         `json:"first"`
	Lines      []remote.Line `json:"lines"`
}

func (a *Agent) finishedDir() string {
	if a.opt.StateDir == "" {
		return ""
	}
	// Its own directory, beside the pid records and never mixed with them:
	// those are read by a cancel across a restart, in a format that must not
	// change under it.
	return filepath.Join(a.opt.StateDir, "finished")
}

func (a *Agent) finishedPath(execID string) string {
	dir := a.finishedDir()
	if dir == "" {
		return ""
	}
	// The id comes from the engine, so it is cleaned before it becomes a path.
	return filepath.Join(dir, filepath.Base(execID)+".json")
}

// persist writes a finished execution.
//
// Called after it moved to `finished`, outside the agent's lock -- a start or
// a resume must not wait behind a disk. The ring no longer changes once the
// execution is closed, so the copy taken here is the outcome.
//
// Failing is NOT fatal, for the reason remember gives: what is lost is this
// execution surviving a restart, and that is worth a line, not a refused step.
func (a *Agent) persist(e *execution) {
	path := a.finishedPath(e.id)
	if path == "" {
		return
	}
	e.mu.Lock()
	rec := finishedRecord{FinishedAt: e.finishedAt, First: e.first,
		Lines: append([]remote.Line(nil), e.lines...)}
	e.mu.Unlock()

	raw, err := json.Marshal(rec)
	if err == nil {
		err = a.writeWhole(path, raw, e)
	}
	if err != nil {
		a.mu.Lock()
		log := a.log
		a.mu.Unlock()
		log.Warn("a finished step could not be written to the state directory; "+
			"it will not survive a restart", "execution", e.id, "error", err)
	}
}

// writeWhole writes beside the target and renames, so a crash mid-write leaves
// a temporary file and never a truncated record.
//
// The rename is under the agent's lock, and only while this execution is still
// the one kept under its id: a prune or a new start of the id may have dropped
// it meanwhile, and writing it then would bring back after a restart what this
// process had already let go.
func (a *Agent) writeWhole(path string, raw []byte, e *execution) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished[e.id] != e {
		_ = os.Remove(tmp.Name())
		return nil
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Restore reads back what the state directory holds, before the agent serves.
//
// It also adopts log for what the agent has to say about that directory later,
// when a write fails. It returns an error only when the directory itself
// cannot be read; a file that is wrong is said and removed, never a reason not
// to start.
func (a *Agent) Restore(log *slog.Logger) error {
	a.mu.Lock()
	a.log = log
	a.mu.Unlock()

	dir := a.finishedDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	restored := 0
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		e, err := readFinished(filepath.Join(dir, name), id, a.opt.RingSize)
		if err != nil {
			log.Warn("a finished step in the state directory could not be read; removing it",
				"file", name, "error", err)
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		a.mu.Lock()
		a.finished[id] = e
		a.mu.Unlock()
		restored++
	}
	if restored > 0 {
		log.Info("finished steps restored from the state directory", "count", restored)
	}
	return nil
}

// readFinished builds an execution that is over: closed, exited and settled --
// nothing runs, nothing will write to it, and no drain waits for it.
func readFinished(path, id string, ring int) (*execution, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec finishedRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	next := rec.First
	if n := len(rec.Lines); n > 0 {
		next = rec.Lines[n-1].Seq + 1
	}
	e := &execution{
		id: id, ring: ring, lines: rec.Lines, first: rec.First, next: next,
		closed: true, exited: true, settled: true, finishedAt: rec.FinishedAt,
		stop: func() {},
	}
	e.cond = sync.NewCond(&e.mu)
	return e, nil
}

// discard is the logger an agent has before Restore gives it one.
var discard = slog.New(slog.NewTextHandler(io.Discard, nil))
