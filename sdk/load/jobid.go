package load

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
	"google.golang.org/api/googleapi"
)

// A load job is named by what it loads, so that a retry of the same batch
// finds the job an earlier attempt left running instead of starting a second
// one.
//
// Issue #85: the gateway stopped waiting on a job after 60 s, the job
// succeeded three seconds later, and the batch was buried as well as written.
// With anonymous jobs every retry was a fresh job -- each one could land, and
// under `write: append` each landing was another copy of the rows.
//
// The name is the hash of the table and the bytes, plus a generation. The
// bytes are the NDJSON, which encoding/json makes deterministic for the same
// envelopes. A job that FAILED has used its name for good, so the next
// attempt takes the next generation.

// maxGenerations bounds how many failed jobs one batch may leave behind
// before the load gives up. A retry policy is a handful of attempts; this is
// a guard against a loop, not a policy.
const maxGenerations = 16

// jobBase is the generation-less name of the job that loads data into table.
func jobBase(table string, data []byte) string {
	h := sha256.New()
	h.Write([]byte(table))
	h.Write([]byte{0})
	h.Write(data)
	return fmt.Sprintf("brevis_%x", h.Sum(nil)[:16])
}

// jobOutcome is how a job that finished ended.
type jobOutcome struct {
	rows   []string // per-row diagnostics, when it failed
	failed error    // the job's own failure; nil when it landed
}

// job is what awaitLoad needs of a BigQuery job. bqJob adapts the client's;
// the tests supply their own, because the client's status cannot be built
// outside its package.
type job interface {
	ID() string
	Location() string
	// Seen is the state the job had when it was fetched: whether it had
	// finished, and its failure if it had.
	Seen() (done bool, failed error)
	// Wait blocks until the job ends. The error is the WAIT's; how the job
	// ended is in the outcome.
	Wait(ctx context.Context) (jobOutcome, error)
}

type (
	submitFunc func(ctx context.Context, id string) (job, error)
	findFunc   func(ctx context.Context, id string) (job, error)
)

// awaitLoad runs the job named base, adopting it when it already exists, and
// returns the per-row failures and the name of the job that ran.
func awaitLoad(ctx context.Context, table, base string, submit submitFunc, find findFunc) ([]string, string, error) {
	for gen := 1; gen <= maxGenerations; gen++ {
		id := fmt.Sprintf("%s_g%d", base, gen)

		j, err := submit(ctx, id)
		switch {
		case isAlreadyExists(err):
			// An earlier attempt created it. Its outcome is this batch's.
			j, err = find(ctx, id)
			if err != nil {
				return nil, id, &core.LoadJobError{Table: table, JobID: id, State: core.JobUnknown,
					Err: fmt.Errorf("find load job: %w", err)}
			}
			done, failed := j.Seen()
			if done && failed != nil {
				continue
			}
			if done {
				return nil, id, nil
			}
		case err != nil:
			// Not necessarily absent: the insert can reach the server and
			// its answer not reach us. The next attempt finds out, by name.
			return nil, id, &core.LoadJobError{Table: table, JobID: id, State: core.JobUnknown,
				Err: fmt.Errorf("start load job: %w", err)}
		}

		out, err := j.Wait(ctx)
		if err != nil {
			return nil, id, &core.LoadJobError{Table: table, JobID: id, Location: j.Location(),
				State: core.JobRunning, Err: fmt.Errorf("wait for load job: %w", err)}
		}
		if out.failed != nil {
			return out.rows, id, fmt.Errorf("load job failed: %w", out.failed)
		}
		return nil, id, nil
	}
	return nil, "", fmt.Errorf("load job: %d generations of %s failed; giving up", maxGenerations, base)
}

func isAlreadyExists(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == 409
}

// bqJob is the client's job seen through the job interface.
type bqJob struct{ *bigquery.Job }

func (j bqJob) Seen() (bool, error) {
	s := j.LastStatus()
	if s == nil || !s.Done() {
		return false, nil
	}
	return true, s.Err()
}

func (j bqJob) Wait(ctx context.Context) (jobOutcome, error) {
	status, err := j.Job.Wait(ctx)
	if err != nil {
		return jobOutcome{}, err
	}
	if err := status.Err(); err != nil {
		return jobOutcome{rows: rowErrors(status), failed: err}, nil
	}
	return jobOutcome{}, nil
}

// runNamedLoadJob is runLoadJob with a name: the job that loads data into
// table is the same job however many times it is asked for.
func (l *Loader) runNamedLoadJob(ctx context.Context, loader *bigquery.Loader, table *bigquery.Table, data []byte) ([]string, string, error) {
	loader.WriteDisposition = bigquery.WriteAppend
	name := fmt.Sprintf("%s.%s.%s", table.ProjectID, table.DatasetID, table.TableID)

	submit := func(ctx context.Context, id string) (job, error) {
		loader.JobID = id
		loader.AddJobIDSuffix = false
		j, err := loader.Run(ctx)
		if err != nil {
			return nil, err
		}
		return bqJob{j}, nil
	}
	find := func(ctx context.Context, id string) (job, error) {
		return l.findJob(ctx, id, "")
	}
	return awaitLoad(ctx, table.TableID, jobBase(name, data), submit, find)
}

// findJob fetches a job by name. A job lives in its dataset's location, and
// BigQuery answers 404 for a regional job asked for without it, so an empty
// location is looked up from the dataset.
func (l *Loader) findJob(ctx context.Context, id, location string) (job, error) {
	if location == "" {
		md, err := l.bq.Dataset(l.cfg.Dataset).Metadata(ctx)
		if err != nil {
			return nil, fmt.Errorf("dataset location: %w", err)
		}
		location = md.Location
	}
	j, err := l.bq.JobFromIDLocation(ctx, id, location)
	if err != nil {
		return nil, err
	}
	return bqJob{j}, nil
}

// keepStaged says whether a failed GCS load must leave its staged object in
// place: when the job may still be running, or may exist at all, the object is
// what it reads. What stays behind is collected by the bucket's lifecycle rule,
// which the staging bucket should have anyway.
func keepStaged(err error) bool {
	var je *core.LoadJobError
	return errors.As(err, &je)
}

// reportOf reads what a fetched job had become.
func reportOf(j job) core.JobReport {
	done, failed := j.Seen()
	if !done {
		return core.JobReport{State: core.JobRunning}
	}
	return core.JobReport{State: core.JobDone, Failed: failed}
}

// Job asks BigQuery what the load job named id became. It is how a caller that
// stopped waiting finds out whether the rows landed; location may be empty,
// and is then the dataset's.
func (l *Loader) Job(ctx context.Context, id, location string) (core.JobReport, error) {
	j, err := l.findJob(ctx, id, location)
	if err != nil {
		return core.JobReport{}, err
	}
	return reportOf(j), nil
}
