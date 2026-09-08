package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

// EnvAutoParams carries the whole set as one JSON object. The engine ALSO
// exports one variable per value (BREVIS_AUTO_ADJUSTED_AT and friends) so a
// shell step needs no parser; the SDK reads the object because it wants all of
// them and a single parse cannot half-succeed.
const EnvAutoParams = "BREVIS_AUTO_PARAMS"

// AutoParams are what the engine worked out about this run so the fetcher does
// not have to. Mirror of run.AutoParams on the engine's side; the JSON tags are
// the contract between them.
//
// Every field is zero outside the engine, and a fetcher run by hand should not
// notice this exists -- which is why Now() falls back to the wall clock.
type AutoParams struct {
	// ScheduledAt is the slot this run represents. Nil on a manual run.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`

	// StartedAt is when this attempt began.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// AdjustedAt is the clock to read INSTEAD of time.Now(). See Now().
	AdjustedAt time.Time `json:"adjusted_at"`

	// DelaySeconds is how late this attempt was against its slot.
	DelaySeconds int `json:"delay_seconds"`

	// IntervalStart and IntervalEnd are the window this run covers, end
	// excluded: [start, end). Nil when the workflow has no schedule.
	IntervalStart *time.Time `json:"interval_start,omitempty"`
	IntervalEnd   *time.Time `json:"interval_end,omitempty"`

	// PreviousError says the run before this one did not succeed, and
	// PreviousSuccessAt is the slot of the last one that did -- which is what
	// says how far back a catch-up has to reach.
	PreviousError     bool       `json:"previous_error"`
	PreviousSuccessAt *time.Time `json:"previous_success_at,omitempty"`

	// Date is AdjustedAt as YYYY-MM-DD in UTC: the partition, the folder, the
	// WHERE clause.
	Date string `json:"date"`
}

// Now is the time this run should read.
//
// It is the whole point of the type. A fetcher that calls time.Now() asks the
// vendor for "the last two hours" counted from whenever the pod happened to
// start; on a run the queue delayed by forty minutes, those forty minutes
// belong to no run at all, because the next slot reads its own now() too.
// Nothing fails and the gap is found weeks later.
//
// Outside the engine it IS the wall clock, so a fetcher run by hand behaves as
// it always did.
func (a AutoParams) Now() time.Time {
	if a.AdjustedAt.IsZero() {
		return time.Now().UTC()
	}
	return a.AdjustedAt
}

// Window is the interval this run covers, end excluded.
//
// The second return says whether there is one: a workflow with no schedule has
// no window, and `ok` is what keeps a caller from silently querying the zero
// time -- which selects everything.
func (a AutoParams) Window() (start, end time.Time, ok bool) {
	if a.IntervalStart == nil || a.IntervalEnd == nil {
		return time.Time{}, time.Time{}, false
	}
	return *a.IntervalStart, *a.IntervalEnd, true
}

// AutoParamsFromEnv reads what the engine injected.
//
// Malformed JSON is logged and dropped rather than failing the run: a bad
// environment variable is not worth losing a load over, and Now() still
// answers.
func AutoParamsFromEnv() AutoParams {
	v := os.Getenv(EnvAutoParams)
	if v == "" {
		return AutoParams{}
	}
	var a AutoParams
	if err := json.Unmarshal([]byte(v), &a); err != nil {
		slog.Warn("ignoring malformed auto params", EnvAutoParams, v, "error", err)
		return AutoParams{}
	}
	return a
}

// Args renders the auto params as slog key-value pairs. Only what is set, and
// only what is worth a log line: the clock, the window and the lateness. The
// rest is on the screen.
func (a AutoParams) Args() []any {
	if a.AdjustedAt.IsZero() {
		return nil
	}
	args := []any{"adjusted_at", a.AdjustedAt.Format(time.RFC3339)}
	if a.DelaySeconds > 0 {
		args = append(args, "delay_seconds", a.DelaySeconds)
	}
	if start, end, ok := a.Window(); ok {
		args = append(args,
			"interval_start", start.Format(time.RFC3339),
			"interval_end", end.Format(time.RFC3339))
	}
	if a.PreviousError {
		args = append(args, "previous_error", true)
	}
	return args
}
