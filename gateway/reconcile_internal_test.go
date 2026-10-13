package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// slowJobSink is a BigQuery-like sink whose load job outlives the send window:
// the write gives up with the job named on the error, and Reconcile answers
// with what the job became server side.
type slowJobSink struct {
	landed   bool
	err      error
	hold     bool // Reconcile blocks until its context ends
	asked    bool
	deadline time.Time
}

func (*slowJobSink) Describe() string { return "slow-job" }
func (*slowJobSink) Write(ctx context.Context, _ []sdk.Envelope) (int64, error) {
	<-ctx.Done()
	return 0, &sdk.LoadJobError{Table: "events", JobID: "brevis_x_g1", State: sdk.JobRunning,
		Err: fmt.Errorf("wait for load job: %w", ctx.Err())}
}
func (s *slowJobSink) Reconcile(ctx context.Context, _ error) (bool, error) {
	s.asked = true
	s.deadline, _ = ctx.Deadline()
	if s.hold {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return s.landed, s.err
}

func sendThroughSlowJob(t *testing.T, sink *slowJobSink) (*recordingDead, error) {
	t.Helper()
	restore := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = restore })

	dead := &recordingDead{}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 4, Backoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}},
		sink:    sink,
		dead:    dead,
		metrics: NewMetrics(),
	}
	err := p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})
	return dead, err
}

// Issue #85, end to end: the job the gateway stopped waiting for succeeded,
// and the 88 events were in the table and in the dead letter at once. Asking
// the job before burying is what tells the two apart.
func TestABatchWhoseJobLandedAfterTheWindowIsNotBuried(t *testing.T) {
	sink := &slowJobSink{landed: true}
	dead, err := sendThroughSlowJob(t, sink)

	if err != nil {
		t.Errorf("a batch that landed was reported as failed: %v", err)
	}
	dead.mu.Lock()
	defer dead.mu.Unlock()
	if len(dead.got) != 0 {
		t.Errorf("%d events buried that are also in the table", len(dead.got))
	}
}

func TestABatchWhoseJobIsStillRunningIsBuried(t *testing.T) {
	dead, err := sendThroughSlowJob(t, &slowJobSink{landed: false})
	if err == nil {
		t.Error("a batch that did not land was reported as delivered")
	}
	dead.mu.Lock()
	defer dead.mu.Unlock()
	if len(dead.got) != 1 {
		t.Errorf("the dead letter received %d events, want 1", len(dead.got))
	}
}

// Asking cannot become the outage. The question gets its own short budget,
// from the caller's context -- the send's is spent by definition, the lesson
// of #42 -- and a warehouse that does not answer still ends in the dead
// letter.
func TestAReconcileThatHangsStillEndsInTheDeadLetter(t *testing.T) {
	restore := reconcileTimeout
	reconcileTimeout = 100 * time.Millisecond
	t.Cleanup(func() { reconcileTimeout = restore })

	sink := &slowJobSink{hold: true}
	start := time.Now()
	dead, _ := sendThroughSlowJob(t, sink)

	if !sink.asked {
		t.Fatal("the sink was never asked what its job became")
	}
	if sink.deadline.IsZero() {
		t.Error("the question ran with no deadline")
	}
	if took := time.Since(start); took > reconcileTimeout+2*time.Second {
		t.Errorf("the send took %s: the question outlived its budget", took)
	}
	dead.mu.Lock()
	defer dead.mu.Unlock()
	if dead.refused != nil || len(dead.got) != 1 {
		t.Errorf("the batch did not reach the dead letter (refused=%v, got=%d)", dead.refused, len(dead.got))
	}
}

// A literal, for the reason TestTheDeadLetterBudgetIsShort uses one.
func TestTheReconcileBudgetIsShort(t *testing.T) {
	if reconcileTimeout > 15*time.Second {
		t.Errorf("reconcileTimeout is %s: it runs before the dead letter, "+
			"inside the drain's grace period", reconcileTimeout)
	}
}

func (*slowJobSink) JobOf(cause error, _ map[string]any) (*sdk.LoadJobError, bool) {
	var je *sdk.LoadJobError
	ok := errors.As(cause, &je)
	return je, ok
}

// A batch buried with its job still running carries the job on every row, so
// a replay -- or the console's resend, #86 -- can ask BigQuery whether the
// rows landed instead of writing them a second time.
func TestABuriedRowCarriesItsLoadJob(t *testing.T) {
	dead, _ := sendThroughSlowJob(t, &slowJobSink{landed: false})

	dead.mu.Lock()
	defer dead.mu.Unlock()
	if len(dead.got) != 1 {
		t.Fatalf("the dead letter received %d events, want 1", len(dead.got))
	}
	row := dead.got[0].Payload.(map[string]any)
	if row[ColumnDeadLetterJobID] != "brevis_x_g1" {
		t.Errorf("%s = %v, want brevis_x_g1", ColumnDeadLetterJobID, row[ColumnDeadLetterJobID])
	}
	if row[ColumnDeadLetterJobState] != string(sdk.JobRunning) {
		t.Errorf("%s = %v, want running", ColumnDeadLetterJobState, row[ColumnDeadLetterJobState])
	}
}

// A failure with no job leaves the record as it always was.
func TestABuriedRowWithoutAJobIsUnchanged(t *testing.T) {
	restore := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = restore })

	dead := &recordingDead{}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 1}},
		sink:    blockingSink{},
		dead:    dead,
		metrics: NewMetrics(),
	}
	_ = p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})

	dead.mu.Lock()
	defer dead.mu.Unlock()
	row := dead.got[0].Payload.(map[string]any)
	for _, k := range []string{ColumnDeadLetterJobID, ColumnDeadLetterJobLocation, ColumnDeadLetterJobState} {
		if _, ok := row[k]; ok {
			t.Errorf("a row with no job carries %s", k)
		}
	}
}
