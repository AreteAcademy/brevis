package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// processStart returns when a pid's process began, or the zero time when there
// is no such process.
//
// # Why `ps` and not /proc
//
// /proc/<pid>/stat is exact and Linux-only: field 22 is the start time in clock
// ticks since boot, which then needs the boot time and the tick rate to become
// an instant. That is three readings and two unit conversions to answer a
// question `ps` answers directly, on both the Linux hosts this agent is for and
// the macOS laptop it is developed on.
//
// The cost is a fork, and it is paid once per CANCEL -- not per step, not per
// line. That is the right place to spend it.
//
// # Why it is asked at all
//
// A pid alone cannot identify a process across this agent restarting: the OS
// reuses them. The start time is what makes a recycled pid detectable, and
// refusing to signal one is the difference between cancelling a step and
// killing somebody's database. See cancel.go.
func processStart(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("pid %d is not a process", pid)
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		// `ps` exits non-zero when the pid is gone, which is not an error to
		// report: a process that no longer exists gives a cancel exactly what
		// it wanted. The zero time says so, and the caller reads it that way.
		return time.Time{}, nil
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return time.Time{}, nil
	}

	// "Thu Sep 10 22:26:04 2026", in the machine's local zone. Parsed in Local
	// because that is what `ps` prints -- reading it as UTC would put every
	// comparison hours out and make every pid look recycled.
	started, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", text, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("this host's `ps` printed a start time this agent "+
			"cannot read (%q): %w", text, err)
	}
	return started, nil
}
