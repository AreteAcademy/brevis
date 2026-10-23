package agent_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// #130: Start checked the id under the lock, let the lock go, set the step up
// and spawned it, and only then recorded it. Two starts of one id arriving
// together -- one start sent twice, its response lost -- both passed the check:
// two processes under one id, the second's entry overwriting the first's, and
// one of them running on with nobody able to resume or cancel it.
func TestTwentyStartsOfOneIdRunItOnce(t *testing.T) {
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})

	const n = 20
	var started atomic.Int32
	var refused atomic.Int32
	go_ := make(chan struct{})
	var ready, done sync.WaitGroup
	for range n {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-go_
			stream, err := a.Start(context.Background(), remote.StartRequest{
				Protocol: remote.Protocol, ExecutionID: "sent-twice", NodeID: "step",
				Command: `echo ran >> "` + ran + `"; sleep 0.3`,
			})
			if err != nil {
				if strings.Contains(err.Error(), "already running here") {
					refused.Add(1)
				} else {
					t.Errorf("a start failed for another reason: %v", err)
				}
				return
			}
			started.Add(1)
			_, _ = io.Copy(io.Discard, stream)
		}()
	}
	ready.Wait()
	close(go_)
	done.Wait()

	if s := started.Load(); s != 1 || refused.Load() != n-1 {
		t.Errorf("%d of %d starts of one id ran (%d refused); wanted exactly one", s, n, refused.Load())
	}
	raw, err := os.ReadFile(ran)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(raw), "ran\n"); lines != 1 {
		t.Errorf("the command ran %d times under one id; wanted once", lines)
	}
}

// A start refused while it is set up -- here a secret outside this host's
// allowlist -- leaves the id free. A reservation it left behind would refuse
// every later start of that id as "already running".
func TestAStartRefusedDuringSetupLeavesTheIdFree(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}, AllowedSecrets: []string{"vendor-api"}})
	_, err := a.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "refused-then-run", NodeID: "step", Command: "true",
		Secrets: map[string]string{"DB_PASSWORD": "brevis-database/password"},
	})
	if err == nil {
		t.Fatal("a secret outside the allowlist was not refused")
	}

	stream, err := a.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "refused-then-run", NodeID: "step", Command: "true",
	})
	if err != nil {
		t.Fatalf("the id stayed taken after a refused start: %v", err)
	}
	_, _ = io.Copy(io.Discard, stream)
}

// A cancel can now arrive while the step is set up -- the execution is
// visible from the moment it is reserved, before any process exists. It must
// neither race the setup (-race catches a read of the command while Start
// writes it) nor leave the step running: cancelled during the setup the step
// is never spawned, and cancelled after it the process is stopped.
func TestACancelDuringTheSetupNeverLeavesTheStepRunning(t *testing.T) {
	a := agent.New(agent.Options{Shell: []string{"/bin/sh", "-c"}})
	for i := range 30 {
		id := "cancelled-" + strings.Repeat("x", i)
		stop := make(chan struct{})
		var canceller sync.WaitGroup
		canceller.Add(1)
		go func() {
			defer canceller.Done()
			for {
				_ = a.Cancel(id)
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
		stream, err := a.Start(context.Background(), remote.StartRequest{
			Protocol: remote.Protocol, ExecutionID: id, NodeID: "step", Command: "sleep 30",
		})
		_ = a.Cancel(id) // whatever the race did, the step is told to stop
		close(stop)
		canceller.Wait()
		if err != nil {
			continue // cancelled during the setup: never spawned
		}
		ended := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, stream); close(ended) }()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatalf("start %d: a step cancelled during or after its setup is still running", i)
		}
	}
}
