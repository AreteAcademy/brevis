package load

import (
	"context"
	"errors"
	"strings"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
	"google.golang.org/api/googleapi"
)

// fakeJob is a load job whose outcome the test decides.
type fakeJob struct {
	id     string
	done   bool  // what a lookup saw
	failed error // the job's own failure, once done
	wait   func(context.Context) error
}

func (j *fakeJob) ID() string          { return j.id }
func (j *fakeJob) Location() string    { return "southamerica-east1" }
func (j *fakeJob) Seen() (bool, error) { return j.done, j.failed }
func (j *fakeJob) Wait(ctx context.Context) (jobOutcome, error) {
	if j.wait != nil {
		if err := j.wait(ctx); err != nil {
			return jobOutcome{}, err
		}
	}
	return jobOutcome{}, nil
}

// fakeService is BigQuery's job table: an ID is taken once.
type fakeService struct {
	jobs      map[string]*fakeJob
	submitted []string
	next      func(id string) *fakeJob // the job a fresh submit creates
}

func newFakeService() *fakeService {
	return &fakeService{jobs: map[string]*fakeJob{}}
}

func (s *fakeService) submit(_ context.Context, id string) (job, error) {
	if _, taken := s.jobs[id]; taken {
		return nil, &googleapi.Error{Code: 409, Message: "Already Exists: Job " + id}
	}
	j := &fakeJob{id: id}
	if s.next != nil {
		j = s.next(id)
	}
	s.jobs[id] = j
	s.submitted = append(s.submitted, id)
	return j, nil
}

func (s *fakeService) find(_ context.Context, id string) (job, error) {
	j, ok := s.jobs[id]
	if !ok {
		return nil, &googleapi.Error{Code: 404, Message: "Not found: Job " + id}
	}
	return j, nil
}

func TestTheSameBatchGetsTheSameJobID(t *testing.T) {
	a := jobBase("p.d.t", []byte(`{"id":1}`+"\n"))
	b := jobBase("p.d.t", []byte(`{"id":1}`+"\n"))
	if a != b {
		t.Fatalf("one batch, two job IDs: %s and %s; a retry would start a second job", a, b)
	}
}

func TestADifferentBatchOrTableGetsAnotherJobID(t *testing.T) {
	base := jobBase("p.d.t", []byte(`{"id":1}`))
	if base == jobBase("p.d.t", []byte(`{"id":2}`)) {
		t.Error("two batches share a job ID: the second would be taken for the first")
	}
	if base == jobBase("p.d.other", []byte(`{"id":1}`)) {
		t.Error("two tables share a job ID: the second table would never be written")
	}
}

// Issue #85: attempt 1 stopped waiting, its job went on and succeeded, and a
// retry submitting a NEW job would have written the 88 rows a second time.
// The retry must find the first job and take its outcome.
func TestARetryAdoptsTheJobThatLanded(t *testing.T) {
	svc := newFakeService()
	svc.next = func(id string) *fakeJob {
		return &fakeJob{id: id, wait: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := awaitLoad(ctx, "t", "brevis_x", svc.submit, svc.find)
	if err == nil {
		t.Fatal("a cancelled wait reported success")
	}

	// Server side, the job finished fine after we stopped waiting.
	svc.jobs["brevis_x_g1"].done = true

	_, id, err := awaitLoad(context.Background(), "t", "brevis_x", svc.submit, svc.find)
	if err != nil {
		t.Fatalf("the retry did not adopt the job that landed: %v", err)
	}
	if len(svc.submitted) != 1 {
		t.Errorf("%d jobs submitted for one batch, want 1: %v", len(svc.submitted), svc.submitted)
	}
	if id != "brevis_x_g1" {
		t.Errorf("job id %q, want brevis_x_g1", id)
	}
}

func TestARetryWaitsOnAJobStillRunning(t *testing.T) {
	svc := newFakeService()
	waited := false
	svc.jobs["brevis_x_g1"] = &fakeJob{
		id: "brevis_x_g1",
		wait: func(context.Context) error {
			waited = true
			return nil
		},
	}

	if _, _, err := awaitLoad(context.Background(), "t", "brevis_x", svc.submit, svc.find); err != nil {
		t.Fatal(err)
	}
	if !waited {
		t.Error("the running job was not waited on")
	}
	if len(svc.submitted) != 0 {
		t.Errorf("a second job was submitted while the first ran: %v", svc.submitted)
	}
}

// A job that FAILED has used its ID for good; reusing it would adopt the
// failure forever. The retry moves to the next generation.
func TestAFailedJobBurnsItsIDAndTheRetryMovesOn(t *testing.T) {
	svc := newFakeService()
	svc.jobs["brevis_x_g1"] = &fakeJob{id: "brevis_x_g1", done: true, failed: errors.New("quota")}

	_, id, err := awaitLoad(context.Background(), "t", "brevis_x", svc.submit, svc.find)
	if err != nil {
		t.Fatal(err)
	}
	if id != "brevis_x_g2" {
		t.Errorf("job id %q, want brevis_x_g2", id)
	}
}

// What the gateway needs to ask BigQuery later: the job, where it runs, and
// that it was still going -- carried on the error, which keeps its message
// and its cause.
func TestAnInterruptedWaitNamesTheJob(t *testing.T) {
	svc := newFakeService()
	svc.next = func(id string) *fakeJob {
		return &fakeJob{id: id, wait: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()

	_, _, err := awaitLoad(ctx, "events", "brevis_x", svc.submit, svc.find)

	var je *core.LoadJobError
	if !errors.As(err, &je) {
		t.Fatalf("the error does not carry the job: %v", err)
	}
	if je.JobID != "brevis_x_g1" || je.Location != "southamerica-east1" ||
		je.Table != "events" || je.State != core.JobRunning {
		t.Errorf("job error %+v", je)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("the cause is lost: auto_table tells a starved table by DeadlineExceeded")
	}
	if !strings.Contains(err.Error(), "wait for load job") {
		t.Errorf("the message changed: %q", err)
	}
}
