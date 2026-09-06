package core

import "fmt"

// SourceFailure says which source failed and why.
//
// It exists because on a fan-out of thousands of sources, "the run failed" is
// not information: what fixes it is knowing WHICH failed, so those get
// reprocessed and the others do not. Without it, the next run redoes
// everything -- and on a fan-out of 4,803 sources, redoing the 3,000 that had
// already worked is the real cost.
type SourceFailure struct {
	// Source is the source's Describe(), already free of secrets.
	Source string

	// Err is the message. Text, not error, because this crosses the Result and
	// is serialized by whoever observes the run.
	Err string
}

func (f SourceFailure) String() string { return f.Source + ": " + f.Err }

// FailurePolicy says what a composed source does when one source fails.
type FailurePolicy int

const (
	// AbortOnError stops on the first failure. It is the default, and it stays
	// the default: it is the behaviour the SDK always had, and changing it in
	// silence would make a run that fails today start "working" with half the
	// data.
	AbortOnError FailurePolicy = iota

	// ContinueOnError records which source failed and moves on to the next.
	//
	// It mirrors the policy the load already has: it tolerates a bad row and
	// reports it in ErrorRows. The asymmetry -- the load tolerating and the
	// extract not -- is what this mode fixes.
	//
	// The failures arrive in Stats.FailedSources and in Result.FailedSources. A
	// run that lost 3,000 of 4,803 sources and does not say which is not fault
	// tolerance: it is silent loss.
	ContinueOnError
)

func (p FailurePolicy) String() string {
	if p == ContinueOnError {
		return "continue"
	}
	return "abort"
}

// ErrEverySourceFailed is returned when ContinueOnError tolerated EVERY
// source.
//
// Zero records from N good sources is a result; zero records because all N
// failed is a broken run, and the two must not look the same to whoever reads
// the log.
func ErrEverySourceFailed(n int, first SourceFailure) error {
	return fmt.Errorf("all %d sources failed, and no record was read. The first one: %s",
		n, first)
}
