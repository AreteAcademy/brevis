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

// Unmapped is the map_index of a step that is not mapped, which is most of
// them. It is -1 and not NULL because two NULLs are never equal in SQL, and the
// unique on (run, node, attempt, map_index) would silently stop protecting
// unmapped steps.
const Unmapped = -1

// StepKey identifies one INSTANCE of a step within a run.
//
// A mapped step runs once per element of a list, and each of those runs has its
// own row, its own exit code and its own retry. Keying by node alone was enough
// until `for_each:` existed; it is not any more, and the two places that care
// are the resume (which of them already succeeded) and the screen (how many
// there are).
type StepKey struct {
	Node     string
	MapIndex int
}

// Step is the key of an unmapped step.
func Step(node string) StepKey { return StepKey{Node: node, MapIndex: Unmapped} }

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
