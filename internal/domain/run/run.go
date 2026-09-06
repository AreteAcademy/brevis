package run

import (
	"time"

	"github.com/google/uuid"
)

// Run is one execution of a workflow.
//
// `Definicao` holds the snapshot of the graph at the moment the Run came into
// being. Section 22 of the plan requires it: editing the workflow later must not
// change the meaning of a past run.
type Run struct {
	ID             uuid.UUID
	WorkflowSlug   string
	IdempotencyKey string
	Status         Status
	Attempt        int
	Definicao      []byte

	// TriggerType says why the Run exists (section 12). Without it there is no
	// telling a backfill from a scheduled run while investigating an incident.
	TriggerType string

	// Params are the values used in THIS run. Stored alongside the run because
	// "what parameters did this run with?" is the first question of any backfill
	// investigation.
	Params map[string]string

	// MaxAtivos is the workflow's concurrency limit at the instant of the
	// trigger. A snapshot: lowering the limit later does not change runs that
	// are already queued.
	MaxAtivos int

	// LogicalDate is the slot this Run represents. Nil for a manual trigger,
	// which belongs to no slot at all.
	LogicalDate *time.Time
	CriadoEm    time.Time
	IniciadoEm  *time.Time
	TerminadoEm *time.Time
	Erro        string
}

// TaskRun e a execucao de um no dentro de um Run.
type TaskRun struct {
	ID          uuid.UUID
	RunID       uuid.UUID
	NodeID      string
	Status      Status
	Attempt     int
	ExitCode    *int
	IniciadoEm  *time.Time
	TerminadoEm *time.Time
	Erro        string
}

// Transition moves the Run, validating. It stamps the times here, and not in
// the caller, so that no path exists that changes the state without recording
// when.
func (r *Run) Transition(para Status, agora time.Time) error {
	if err := Valida(r.Status, para); err != nil {
		return err
	}
	r.Status = para

	switch para {
	case StatusRunning:
		if r.IniciadoEm == nil {
			r.IniciadoEm = &agora
		}
	case StatusSuccess, StatusCanceled:
		r.TerminadoEm = &agora
	case StatusQueued:
		// requeued by a retry: the run starts over, so the previous stamps no
		// longer hold for the new attempt
		r.IniciadoEm, r.TerminadoEm = nil, nil
	}
	return nil
}

// Transition moves the TaskRun, with the same validation.
func (t *TaskRun) Transition(para Status, agora time.Time) error {
	if err := Valida(t.Status, para); err != nil {
		return err
	}
	t.Status = para

	switch para {
	case StatusRunning:
		if t.IniciadoEm == nil {
			t.IniciadoEm = &agora
		}
	case StatusSuccess, StatusFailed, StatusCanceled:
		t.TerminadoEm = &agora
	}
	return nil
}
