package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

type rep string

func (r rep) String() string { return string(r) }

func TestRunPruneRunsAtStartAndOnEveryTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	calls := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		RunPrune(ctx, tick, func(context.Context) (fmt.Stringer, error) {
			calls <- struct{}{}
			if len(calls) == 2 {
				return nil, errors.New("boom") // a failure must not stop the loop
			}
			return rep("ok"), nil
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	<-calls // the start
	tick <- time.Now()
	<-calls
	tick <- time.Now()
	<-calls
	cancel()
	<-done
}
