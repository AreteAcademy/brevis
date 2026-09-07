package run

import (
	"time"

	"github.com/google/uuid"
)

// Run is one execution of a workflow.
//
// `Definition` holds the snapshot of the graph at the moment the Run came into
// being. Section 22 of the plan requires it: editing the workflow later must not
// change the meaning of a past run.
type Run struct {
	ID             uuid.UUID
	WorkflowSlug   string
	IdempotencyKey string
	Status         Status
	Attempt        int
	Definition     []byte

	// TriggerType says why the Run exists (section 12). Without it there is no
	// telling a backfill from a scheduled run while investigating an incident.
	TriggerType string

	// Params are the values used in THIS run. Stored alongside the run because
	// "what parameters did this run with?" is the first question of any backfill
	// investigation.
	Params map[string]string

	// MaxActive is the workflow's concurrency limit at the instant of the
	// trigger. A snapshot: lowering the limit later does not change runs that
	// are already queued.
	MaxActive int

	// LogicalDate is the slot this Run represents. Nil for a manual trigger,
	// which belongs to no slot at all.
	LogicalDate *time.Time
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Err         string
}

// TaskRun is the execution of one node inside a Run.
type TaskRun struct {
	ID         uuid.UUID
	RunID      uuid.UUID
	NodeID     string
	Status     Status
	Attempt    int
	ExitCode   *int
	StartedAt  *time.Time
	FinishedAt *time.Time
	Err        string
}

// Transition moves the Run, validating. It stamps the times here, and not in
// the caller, so that no path exists that changes the state without recording
// when.
func (r *Run) Transition(para Status, now time.Time) error {
	if err := Validate(r.Status, para); err != nil {
		return err
	}
	r.Status = para

	switch para {
	case StatusRunning:
		if r.StartedAt == nil {
			r.StartedAt = &now
		}
	case StatusSuccess, StatusCanceled:
		r.FinishedAt = &now
	case StatusQueued:
		// requeued by a retry: the run starts over, so the previous stamps no
		// longer hold for the new attempt
		r.StartedAt, r.FinishedAt = nil, nil
	}
	return nil
}

// Transition moves the TaskRun.
//
// Against the STEP's graph, not the run's. The two shared one until `skipped`
// arrived -- a state a step has and a run does not -- and a single map could no
// longer say the truth about both.
func (t *TaskRun) Transition(para Status, now time.Time) error {
	if err := ValidateStep(t.Status, para); err != nil {
		return err
	}
	t.Status = para

	switch para {
	case StatusRunning:
		if t.StartedAt == nil {
			t.StartedAt = &now
		}
	case StatusSuccess, StatusFailed, StatusCanceled, StatusSkipped:
		t.FinishedAt = &now
	}
	return nil
}
