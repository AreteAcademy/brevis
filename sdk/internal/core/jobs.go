package core

// JobState is what was last known about a load job that a write stopped
// waiting for.
type JobState string

const (
	// JobRunning: the job was created and had not finished when the wait
	// ended. It may still succeed.
	JobRunning JobState = "running"
	// JobUnknown: creating or finding the job failed in a way that does not
	// say whether it exists. Asking for it by ID is the only way to know.
	JobUnknown JobState = "unknown"
)

// LoadJobError is a write that ended before its load job did.
//
// The job keeps running on the server after the client stops waiting, and it
// can succeed: issue #85 buried 88 rows that its job had written three seconds
// earlier. So the error names the job, which is what lets the caller ask the
// warehouse what happened instead of assuming the worst.
//
// It wraps the cause unchanged: the message reads as before, and
// errors.Is(err, context.DeadlineExceeded) still holds.
type LoadJobError struct {
	Table    string
	JobID    string
	Location string
	State    JobState
	Err      error
}

func (e *LoadJobError) Error() string { return e.Err.Error() }
func (e *LoadJobError) Unwrap() error { return e.Err }
