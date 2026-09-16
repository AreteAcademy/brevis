package sdk_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// The hook runs after a load that worked, and gets the Result.
func TestAfterRunsWithTheResult(t *testing.T) {
	var got *sdk.Result
	p := afterPipeline(&got, nil)

	if err := sdk.Execute(context.Background(), &p, nil); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("After did not run on a load that worked")
	}
	if got.Records != 2 {
		t.Errorf("the hook saw Records=%d, want 2", got.Records)
	}
}

// It must NOT run when the load failed: a value derived from a run that broke
// describes a run that did not happen.
func TestAfterIsSkippedWhenTheLoadFailed(t *testing.T) {
	var got *sdk.Result
	p := afterPipeline(&got, nil)
	p.Target.To = failingTarget{}

	if err := sdk.Execute(context.Background(), &p, nil); err == nil {
		t.Fatal("the failing load reported success")
	}
	if got != nil {
		t.Error("After ran after a failed load")
	}
}

// Its error fails the step. A value that never reached the next run is a reason
// to retry: the run after it reads a stale key and nothing says the publish was
// skipped.
func TestAfterFailingFailsTheRun(t *testing.T) {
	var got *sdk.Result
	p := afterPipeline(&got, fmt.Errorf("the bucket said no"))

	err := sdk.Execute(context.Background(), &p, nil)
	if err == nil {
		t.Fatal("a failing After left the run green")
	}
	if !strings.Contains(err.Error(), "the bucket said no") {
		t.Errorf("the error lost the cause: %v", err)
	}
}

// The reason the hook takes a Result rather than living in main: with
// ContinueOnError a run SUCCEEDS having skipped sources, and only here is that
// visible in time to refuse.
func TestAfterCanSeeThatSourcesWereSkipped(t *testing.T) {
	var seen int
	published := false

	p := sdk.Pipeline{
		Name: "after under test",
		Source: sdk.Source{From: from.Many{
			Sources: []sdk.Reader{
				rowSource{n: 2},
				brokenSource{},
			},
			OnError: sdk.ContinueOnError,
		}},
		Target: sdk.Target{To: countingSink{}, Columns: []string{"i"}},
		After: func(_ context.Context, _ *sdk.Pipeline, res *sdk.Result) error {
			seen = len(res.FailedSources)
			published = true
			return nil
		},
	}

	if err := sdk.Execute(context.Background(), &p, nil); err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("After did not run")
	}
	if seen != 1 {
		t.Errorf("the hook saw %d failed sources, want 1 -- without that number a "+
			"caller cannot tell a whole list from a short one", seen)
	}
}

// Nothing configured: a pipeline with no After behaves exactly as before.
func TestNoAfterChangesNothing(t *testing.T) {
	var never *sdk.Result
	p := afterPipeline(&never, nil)
	p.After = nil

	if err := sdk.Execute(context.Background(), &p, nil); err != nil {
		t.Fatal(err)
	}
	if never != nil {
		t.Error("something ran")
	}
}

func afterPipeline(got **sdk.Result, hookErr error) sdk.Pipeline {
	return sdk.Pipeline{
		Name:   "after under test",
		Source: sdk.Source{From: rowSource{n: 2}},
		Target: sdk.Target{To: countingSink{}, Columns: []string{"i"}},
		After: func(_ context.Context, _ *sdk.Pipeline, res *sdk.Result) error {
			*got = res
			return hookErr
		},
	}
}

type rowSource struct{ n int }

func (rowSource) Describe() string { return "rows" }
func (r rowSource) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		for i := 0; i < r.n; i++ {
			if !yield(sdk.Envelope{Payload: map[string]any{"i": float64(i)}}, nil) {
				return
			}
		}
	}, nil
}

type countingSink struct{}

func (countingSink) Describe() string { return "sink" }
func (countingSink) Write(_ context.Context, e []sdk.Envelope, _ sdk.WriteOptions) (*sdk.LoadResult, error) {
	return &sdk.LoadResult{RowsLoaded: int64(len(e))}, nil
}

type failingTarget struct{}

func (failingTarget) Describe() string { return "a sink that refuses" }
func (failingTarget) Write(context.Context, []sdk.Envelope, sdk.WriteOptions) (*sdk.LoadResult, error) {
	return nil, fmt.Errorf("the table is gone")
}

type brokenSource struct{}

func (brokenSource) Describe() string { return "a source that will not open" }
func (brokenSource) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return nil, fmt.Errorf("this one is down")
}
