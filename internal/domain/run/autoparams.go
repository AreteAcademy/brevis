package run

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// AutoParams are what the ENGINE knows about a run and every pipeline would
// otherwise work out for itself.
//
// The one that matters is AdjustedAt, and the bug it removes is common enough
// to be worth naming. A fetcher reads `time.Now()`, subtracts its window and
// asks the vendor for the last two hours. On a run that starts on time that is
// right. On a run the queue delayed by forty minutes it is forty minutes wrong
// -- and the forty minutes it skipped belong to no run at all, because the next
// slot reads its own `now()` too. The data is simply missing, nothing failed,
// and it is found weeks later.
//
// So the engine hands over a clock instead: AdjustedAt is what a pipeline
// should read INSTEAD of now(). For a scheduled run it is the slot, so it does
// not move when the run is late and does not move when the run is retried three
// hours later. For a manual run there is no slot, so it is when the run
// started -- which is the only honest answer.
//
// They are a SNAPSHOT, computed when the run starts and stored on it. Not
// derived on read: PreviousError is a fact about the moment this run began, and
// recomputing it tomorrow -- after the previous run was retried and passed --
// would answer a different question with the same name.
type AutoParams struct {
	// ScheduledAt is the slot this run represents: what the cron said. Absent
	// on a manual run, which belongs to no slot.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`

	// StartedAt is when this ATTEMPT began.
	//
	// Named for the column it comes from (`iniciado_em`) and for what every
	// other orchestrator calls it, rather than `running_at`: a run is not
	// running for the whole time this timestamp describes -- it is the instant
	// it started, and a retry moves it.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// AdjustedAt is the clock to read instead of now(). See the type's comment.
	AdjustedAt time.Time `json:"adjusted_at"`

	// DelaySeconds is how late this attempt was: StartedAt minus ScheduledAt.
	// Zero on a manual run and never negative.
	//
	// It is here so nobody subtracts two timestamps to find out, and because a
	// pipeline that decides to widen its window when it is very late needs one
	// number rather than two.
	DelaySeconds int `json:"delay_seconds"`

	// IntervalStart and IntervalEnd are the window this run covers: from the
	// PREVIOUS slot up to this one, end excluded.
	//
	// A pipeline that asks the vendor for [start, end) never overlaps and never
	// gaps, however late it runs and however often it retries. That is the
	// whole point, and it is why they come from the CRON and not from the
	// history: a backfill of a slot from March has to produce the window March
	// had, not the window this workflow's runs happen to describe today.
	//
	// Absent when the workflow has no schedule.
	IntervalStart *time.Time `json:"interval_start,omitempty"`
	IntervalEnd   *time.Time `json:"interval_end,omitempty"`

	// PreviousError says the previous run of this workflow did not succeed.
	PreviousError bool `json:"previous_error"`

	// PreviousSuccessAt is the slot of the last run that DID succeed, which is
	// what makes PreviousError actionable: a boolean says something is wrong, a
	// timestamp says how far back to catch up.
	PreviousSuccessAt *time.Time `json:"previous_success_at,omitempty"`

	// Date is AdjustedAt as YYYY-MM-DD, in UTC. It is here because it is the
	// single most-used value in the whole of data engineering -- a partition, a
	// folder, a WHERE clause -- and because formatting it in eight pipelines is
	// eight chances to pick a different timezone.
	Date string `json:"date"`
}

// Previous is what the history says about the run before this one.
type Previous struct {
	Failed    bool
	SuccessAt *time.Time
}

// Interval is the window a schedule gives this slot. Zero means the workflow
// has no schedule.
type Interval struct{ Start, End time.Time }

// Auto computes a run's automatic params.
//
// Pure: everything it needs is passed in, so the rule can be read and tested
// without a database and without a clock.
func Auto(r Run, started time.Time, prev Previous, window Interval) AutoParams {
	a := AutoParams{
		PreviousError:     prev.Failed,
		PreviousSuccessAt: prev.SuccessAt,
	}
	if !started.IsZero() {
		at := started.UTC()
		a.StartedAt = &at
	}
	if r.LogicalDate != nil {
		at := r.LogicalDate.UTC()
		a.ScheduledAt = &at
	}

	// The clock. A slot when there is one, because it does not move; the start
	// otherwise, because there is nothing else true.
	switch {
	case a.ScheduledAt != nil:
		a.AdjustedAt = *a.ScheduledAt
	case a.StartedAt != nil:
		a.AdjustedAt = *a.StartedAt
	default:
		a.AdjustedAt = started.UTC()
	}

	if a.ScheduledAt != nil && a.StartedAt != nil {
		if late := a.StartedAt.Sub(*a.ScheduledAt); late > 0 {
			a.DelaySeconds = int(late.Seconds())
		}
	}

	if !window.Start.IsZero() && !window.End.IsZero() {
		s, e := window.Start.UTC(), window.End.UTC()
		a.IntervalStart, a.IntervalEnd = &s, &e
	}

	a.Date = a.AdjustedAt.Format("2006-01-02")
	return a
}

// Env is the auto params as environment variables, one each.
//
// One variable per value, and not only the JSON blob: a `bash` step with no
// library reads `$BREVIS_AUTO_ADJUSTED_AT`, and requiring `jq` to answer "what
// time is it for this run" would put a dependency in the way of the simplest
// possible step.
func (a AutoParams) Env() map[string]string {
	out := map[string]string{
		"BREVIS_AUTO_ADJUSTED_AT":    a.AdjustedAt.Format(time.RFC3339),
		"BREVIS_AUTO_DATE":           a.Date,
		"BREVIS_AUTO_DELAY_SECONDS":  strconv.Itoa(a.DelaySeconds),
		"BREVIS_AUTO_PREVIOUS_ERROR": strconv.FormatBool(a.PreviousError),
	}
	// The optional ones are OMITTED rather than set empty. A variable that is
	// always there and sometimes blank makes every reader write the same
	// two-line check; an absent one makes the shell's `${X:-default}` work.
	for name, when := range map[string]*time.Time{
		"BREVIS_AUTO_SCHEDULED_AT":        a.ScheduledAt,
		"BREVIS_AUTO_STARTED_AT":          a.StartedAt,
		"BREVIS_AUTO_INTERVAL_START":      a.IntervalStart,
		"BREVIS_AUTO_INTERVAL_END":        a.IntervalEnd,
		"BREVIS_AUTO_PREVIOUS_SUCCESS_AT": a.PreviousSuccessAt,
	} {
		if when != nil {
			out[name] = when.Format(time.RFC3339)
		}
	}
	// dlt's contract for an external scheduler, which is the reason this bridge
	// is two variables and not a library.
	//
	// dlt tracks incrementality in state it persists in the destination, and
	// that state only moves FORWARD. A backfill of a slot from March therefore
	// reads whatever the cursor says today, which is the wrong window, and the
	// blunt instrument dlt offers for it is dropping the state
	// (`drop_data`, `drop_resources`). The engine already knows the right
	// answer: IntervalStart and IntervalEnd come from the CRON, so the March
	// run produces March's window however often it is retried.
	//
	// dlt reads these before it tries Airflow, in
	// `dlt/extract/incremental/context.py` -- `DLT_INTERVAL_START` and
	// `DLT_INTERVAL_END`, UTC ISO 8601, and a resource opts in with
	// `allow_external_schedulers=True`. Set, they become `initial_value` and
	// `end_value`, and an incremental with an `end_value` does not touch the
	// persisted state at all: the backfill is stateless, which is exactly what
	// makes re-running one slot idempotent.
	//
	// The half-open range matches on both sides -- Brevis is [start, end) with
	// the end excluded, and so is dlt (`range_start` closed, `range_end` open).
	// If either side ever changes that, the test in autoparams_test.go says so.
	//
	// Both or neither, which is also dlt's rule: it resolves a PARTIAL
	// interval to nothing and then raises rather than quietly falling back to
	// its own state. Here they are set together by Auto, so the pairing holds
	// by construction rather than by care.
	//
	// DLT_INTERVAL_TIMEZONE is deliberately NOT set. It would re-stamp both
	// datetimes with another zone's identity, and every timestamp this engine
	// produces is UTC.
	if a.IntervalStart != nil && a.IntervalEnd != nil {
		out["DLT_INTERVAL_START"] = a.IntervalStart.Format(time.RFC3339)
		out["DLT_INTERVAL_END"] = a.IntervalEnd.Format(time.RFC3339)
	}

	// And the whole thing, for a step that would rather parse one value.
	if raw, err := json.Marshal(a); err == nil {
		out["BREVIS_AUTO_PARAMS"] = string(raw)
	}
	return out
}

// String is what the screen shows for one value, so the UI and a log line
// agree.
func (a AutoParams) String() string {
	return fmt.Sprintf("adjusted_at=%s delay=%ds previous_error=%t",
		a.AdjustedAt.Format(time.RFC3339), a.DelaySeconds, a.PreviousError)
}
