package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	hold     bool  // Reconcile blocks until its context ends
	learned  error // what Reconcile found out; the cause when nil
	asked    bool
	deadline time.Time
}

func (*slowJobSink) Describe() string { return "slow-job" }
func (*slowJobSink) Write(ctx context.Context, _ []sdk.Envelope) (int64, error) {
	<-ctx.Done()
	return 0, &sdk.LoadJobError{Table: "events", JobID: "brevis_x_g1", State: sdk.JobRunning,
		Err: fmt.Errorf("wait for load job: %w", ctx.Err())}
}
func (s *slowJobSink) Reconcile(ctx context.Context, cause error) (Reconciliation, error) {
	s.asked = true
	s.deadline, _ = ctx.Deadline()
	if s.hold {
		<-ctx.Done()
		return Reconciliation{Cause: cause}, ctx.Err()
	}
	if s.learned != nil {
		return Reconciliation{Landed: s.landed, Cause: s.learned}, s.err
	}
	return Reconciliation{Landed: s.landed, Cause: cause}, s.err
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

// A reconciled batch is a delivered batch in the numbers too. Counted as
// buried, the dashboard would report the #85 incident that did not happen.
func TestAReconciledBatchCountsAsDelivered(t *testing.T) {
	restore := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = restore })

	m := NewMetrics()
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 1}},
		sink:    &slowJobSink{landed: true},
		dead:    &recordingDead{},
		metrics: m,
	}
	_ = p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})

	var b strings.Builder
	if err := m.Render(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `brevis_gateway_batches_total{stream="ingestion",sink="slow-job",outcome="delivered"} 1`) {
		t.Errorf("the reconciled batch is not counted as delivered:\n%s", out)
	}
	if strings.Contains(out, `outcome="buried"`) {
		t.Errorf("the reconciled batch is counted as buried:\n%s", out)
	}
}

// BigQuery answers 404 for a regional job asked for without its location, so
// a row that names its job and not where it runs names a job nobody can find.
func TestABuriedRowCarriesWhereItsJobRuns(t *testing.T) {
	restore := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = restore })

	dead := &recordingDead{}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 1}},
		sink:    locatedJobSink{},
		dead:    dead,
		metrics: NewMetrics(),
	}
	_ = p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})

	dead.mu.Lock()
	defer dead.mu.Unlock()
	row := dead.got[0].Payload.(map[string]any)
	if row[ColumnDeadLetterJobLocation] != "southamerica-east1" {
		t.Errorf("%s = %v, want southamerica-east1", ColumnDeadLetterJobLocation, row[ColumnDeadLetterJobLocation])
	}
}

// locatedJobSink gives up with a job that names its location, and cannot be
// reconciled.
type locatedJobSink struct{}

func (locatedJobSink) Describe() string { return "located-job" }
func (locatedJobSink) Write(ctx context.Context, _ []sdk.Envelope) (int64, error) {
	<-ctx.Done()
	return 0, &sdk.LoadJobError{JobID: "j", Location: "southamerica-east1", State: sdk.JobRunning, Err: ctx.Err()}
}
func (locatedJobSink) JobOf(cause error, _ map[string]any) (*sdk.LoadJobError, bool) {
	var je *sdk.LoadJobError
	ok := errors.As(cause, &je)
	return je, ok
}

// flakySink is #85's double outcome by another road: attempt 1 leaves a load
// job behind, and the LAST attempt fails before it reaches the load -- a
// metadata call, say -- with an error that names no job.
type flakySink struct {
	mu       sync.Mutex
	calls    int
	asked    error // the cause Reconcile was handed
	landed   bool
	learned  error // what Reconcile reports it learned; the cause when nil
	attempts int
}

func (*flakySink) Describe() string { return "flaky" }
func (f *flakySink) Write(context.Context, []sdk.Envelope) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return 0, &sdk.LoadJobError{Table: "events", JobID: "brevis_x_g1", Location: "US",
			State: sdk.JobUnknown, Err: errors.New("start load job: 503")}
	}
	return 0, errors.New("table metadata: 503")
}
func (f *flakySink) Reconcile(_ context.Context, cause error) (Reconciliation, error) {
	f.asked = cause
	if f.learned != nil {
		return Reconciliation{Landed: f.landed, Cause: f.learned}, nil
	}
	return Reconciliation{Landed: f.landed, Cause: cause}, nil
}
func (*flakySink) JobOf(cause error, _ map[string]any) (*sdk.LoadJobError, bool) {
	var je *sdk.LoadJobError
	ok := errors.As(cause, &je)
	return je, ok
}

func sendThroughFlaky(t *testing.T, sink *flakySink) (*recordingDead, error) {
	t.Helper()
	dead := &recordingDead{}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 2, Backoff: time.Millisecond, MaxBackoff: time.Millisecond}},
		sink:    sink,
		dead:    dead,
		metrics: NewMetrics(),
	}
	err := p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})
	return dead, err
}

// The job is a function of the batch, so asking about attempt 1's job is
// asking about this batch.
func TestAnEarlierAttemptsJobIsStillAskedAbout(t *testing.T) {
	sink := &flakySink{landed: true}
	dead, err := sendThroughFlaky(t, sink)

	var je *sdk.LoadJobError
	if !errors.As(sink.asked, &je) || je.JobID != "brevis_x_g1" {
		t.Fatalf("Reconcile was asked about %v, not attempt 1's job", sink.asked)
	}
	if err != nil {
		t.Errorf("attempt 1's job landed, and the batch was reported failed: %v", err)
	}
	dead.mu.Lock()
	defer dead.mu.Unlock()
	if len(dead.got) != 0 {
		t.Errorf("%d events buried that attempt 1 wrote", len(dead.got))
	}
}

func TestAnEarlierAttemptsJobIsStampedWhenItDidNotLand(t *testing.T) {
	dead, _ := sendThroughFlaky(t, &flakySink{landed: false})

	dead.mu.Lock()
	defer dead.mu.Unlock()
	row := dead.got[0].Payload.(map[string]any)
	if row[ColumnDeadLetterJobID] != "brevis_x_g1" {
		t.Errorf("%s = %v: the job that may still land is not on the row",
			ColumnDeadLetterJobID, row[ColumnDeadLetterJobID])
	}
	if !strings.Contains(row["_dead_letter_reason"].(string), "table metadata: 503") {
		t.Errorf("the reason lost the last attempt's failure: %v", row["_dead_letter_reason"])
	}
}

// What the question learned is what the row says. The job was found failed:
// "running" would send whoever replays it to ask again, for nothing.
func TestTheRowSaysWhatReconcileLearned(t *testing.T) {
	failed := &sdk.LoadJobError{Table: "events", JobID: "brevis_x_g1", Location: "US",
		State: sdk.JobDone, Err: errors.New("load job failed: invalid row")}
	dead, _ := sendThroughFlaky(t, &flakySink{learned: failed})

	dead.mu.Lock()
	defer dead.mu.Unlock()
	row := dead.got[0].Payload.(map[string]any)
	if row[ColumnDeadLetterJobState] != string(sdk.JobDone) {
		t.Errorf("%s = %v, want done", ColumnDeadLetterJobState, row[ColumnDeadLetterJobState])
	}
}

// "Why is this here" is the first question about a buried row. When the job
// turned out to have FAILED, that failure is the answer -- not the timeout
// that made the gateway stop waiting for it.
func TestTheReasonIsTheJobsFailureWhenThatIsWhatWasLearned(t *testing.T) {
	failed := &sdk.LoadJobError{Table: "events", JobID: "brevis_x_g1", State: sdk.JobDone,
		Err: errors.New("load job failed: invalid row 7")}
	dead, _ := sendThroughSlowJob(t, &slowJobSink{learned: failed})

	dead.mu.Lock()
	defer dead.mu.Unlock()
	reason, _ := dead.got[0].Payload.(map[string]any)["_dead_letter_reason"].(string)
	if !strings.Contains(reason, "invalid row 7") {
		t.Errorf("the reason does not say why the job failed: %q", reason)
	}
}
