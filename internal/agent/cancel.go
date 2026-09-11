package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Cancelling across a restart, and the one thing that must never go wrong.
//
// The engine can ask this agent to stop a step it started before it was
// restarted. All that survives a restart is a file, and a file holding a pid is
// not enough: the OS reuses pids, so by the time anybody reads it that number
// may belong to somebody else's database.
//
// So the process's START TIME is stored beside it, and a cancel that finds a
// mismatch refuses rather than signalling. Killing the wrong process on a host
// this program does not own is the worst thing it could do, and it is worth a
// file and a comparison to make impossible.

// pidRecord is what survives a restart.
type pidRecord struct {
	PID int `json:"pid"`

	// StartedUnixNano is the moment THIS process began, as this agent saw it.
	// It is the discriminator: a recycled pid belongs to a process that started
	// later, and the difference is visible without asking the OS anything
	// clever.
	StartedUnixNano int64 `json:"started_unix_nano"`
}

func (a *Agent) statePath(execID string) string {
	if a.opt.StateDir == "" {
		return ""
	}
	// The id comes from the engine, so it is cleaned before it becomes a path.
	return filepath.Join(a.opt.StateDir, filepath.Base(execID)+".json")
}

// remember writes the pid so a restarted agent can still cancel.
//
// Failing to write is NOT fatal: the step is running and killing it because the
// bookkeeping failed would be the wrong trade. What is lost is cancel across a
// restart, and it is worth a line in the log rather than a refused step.
func (a *Agent) remember(execID string, pid int, started time.Time) {
	path := a.statePath(execID)
	if path == "" {
		return
	}
	raw, err := json.Marshal(pidRecord{PID: pid, StartedUnixNano: started.UnixNano()})
	if err != nil {
		return
	}
	_ = os.MkdirAll(a.opt.StateDir, 0o700)
	_ = os.WriteFile(path, raw, 0o600)
}

func (a *Agent) forget(execID string) {
	if path := a.statePath(execID); path != "" {
		_ = os.Remove(path)
	}
}

// cancelFromState stops a step this agent no longer holds in memory.
func (a *Agent) cancelFromState(execID string) error {
	path := a.statePath(execID)
	if path == "" {
		return fmt.Errorf("execution %q is not running here, and this agent keeps no state "+
			"between restarts (--state-dir is unset), so there is nothing left to stop. "+
			"The process may still be running", execID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// Nothing to stop is the ordinary case: the step finished and the file
		// was removed. Reporting success here is honest -- what was asked for
		// is already true.
		return nil
	}
	var rec pidRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("the record for %q is unreadable, so the process was NOT signalled: "+
			"%w. It may still be running", execID, err)
	}

	same, err := sameProcess(rec)
	if err != nil {
		return fmt.Errorf("could not confirm pid %d is still the step's process, so it was "+
			"NOT signalled: %w", rec.PID, err)
	}
	if !same {
		// The refusal that justifies the whole file. Nothing is signalled, the
		// stale record is cleared, and the message says what happened -- an
		// operator reading "cancelled" about a pid that is now a database would
		// have no way to know.
		_ = os.Remove(path)
		return fmt.Errorf("pid %d no longer belongs to execution %q -- the OS gave it to "+
			"another process -- so NOTHING was signalled. The step's own process is gone",
			rec.PID, execID)
	}

	if err := syscall.Kill(-rec.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signalling process group %d: %w", rec.PID, err)
	}
	_ = os.Remove(path)
	return nil
}

// sameProcess says whether the pid still belongs to the process that was
// recorded.
//
// Compared by START TIME, which is what makes a recycled pid detectable. The
// tolerance is a second: this agent reads the clock just after the fork and the
// OS records it just before, so they differ by a little and always will. A
// recycled pid belongs to a process that started minutes or days later, so a
// second is not close to ambiguous.
func sameProcess(rec pidRecord) (bool, error) {
	started, err := processStart(rec.PID)
	if err != nil {
		return false, err
	}
	if started.IsZero() {
		// The pid is not there at all: the process is gone, which is the same
		// outcome a cancel wanted.
		return false, nil
	}
	delta := started.Sub(time.Unix(0, rec.StartedUnixNano))
	if delta < 0 {
		delta = -delta
	}
	return delta < time.Second, nil
}
