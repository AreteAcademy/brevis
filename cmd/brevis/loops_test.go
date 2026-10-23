package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// #122: `brevis scheduler` returned as soon as the FIRST of its two loops did.
// The scheduler loop returns the instant the context ends; the dispatcher is
// still waiting for its runs to write their end -- and the deferred pool.Close
// ran under it, so a shutdown never wrote anything down ("closed pool").
func TestTheSchedulerWaitsForItsSlowestLoop(t *testing.T) {
	var slowDone atomic.Bool
	fast := func() error { return nil }
	slow := func() error {
		time.Sleep(50 * time.Millisecond)
		slowDone.Store(true)
		return nil
	}

	if err := runLoops(fast, slow); err != nil {
		t.Fatal(err)
	}
	if !slowDone.Load() {
		t.Error("runLoops returned while a loop was still running; the pool would close under it")
	}
}

// And what one loop reports is not lost because the other finished cleanly.
func TestALoopsErrorIsReturnedAfterBothEnd(t *testing.T) {
	broken := errors.New("dispatcher: database gone")
	fast := func() error { return nil }
	slow := func() error { time.Sleep(20 * time.Millisecond); return broken }

	if err := runLoops(fast, slow); !errors.Is(err, broken) {
		t.Errorf("runLoops = %v; wanted the slow loop's error", err)
	}
}
