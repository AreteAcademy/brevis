package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/runcontext"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// The two halves of the return path, set together.
//
// This is the gap that made the whole feature work locally and do nothing in a
// pod: the runner picked an output path meaningful on ITS filesystem, the pod
// never learned about it, and a step's context.set() wrote to a file nobody
// would ever read. No error anywhere -- the step succeeded, and the step below
// found nothing.
func TestThePodTellsTheStepWhereToPublishAndReadsItBack(t *testing.T) {
	pod, err := BuildPod(execution.TaskExec{
		NodeID: "extract", Image: "python:3.12", Command: "python fetch.py",
		Shell: true, RunID: "r", Workflow: "w",
		// The runner's path, which means nothing inside a container.
		OutputPath: "/tmp/engine-side/extract-0.json",
		Env:        map[string]string{runcontext.EnvOutput: "/tmp/engine-side/extract-0.json"},
	}, Options{}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}

	c := pod.Spec.Containers[0]
	if c.TerminationMessagePath != "/dev/termination-log" {
		t.Errorf("terminationMessagePath = %q; without it the kubelet copies "+
			"nothing into the status and the context never comes back",
			c.TerminationMessagePath)
	}

	var told string
	for _, v := range c.Env {
		if v.Name == runcontext.EnvOutput {
			told = v.Value
		}
	}
	if told != c.TerminationMessagePath {
		t.Errorf("the step is told to write to %q and the kubelet reads %q. A step "+
			"writing where nobody reads is the silent failure this feature is against",
			told, c.TerminationMessagePath)
	}
}

// A step that publishes nothing keeps the pod it had before this feature.
func TestAStepWithNoOutputPathGetsNoTerminationMessagePath(t *testing.T) {
	pod, err := BuildPod(execution.TaskExec{
		NodeID: "a", Image: "alpine", Command: "echo hi", Shell: true,
		RunID: "r", Workflow: "w",
	}, Options{}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if p := pod.Spec.Containers[0].TerminationMessagePath; p != "" {
		t.Errorf("terminationMessagePath = %q on a step that publishes nothing", p)
	}
}

// And the way back: the message in the pod's status IS the context.
func TestPublishedContextComesFromTheTerminatedMessage(t *testing.T) {
	published := `{"bucket":"s3://landing","rows":48213}`
	pod := podWithTerminated(published, 0)

	got := pod.PublishedContext()
	if got != published {
		t.Fatalf("PublishedContext() = %q, want %q", got, published)
	}
	// And it is what runcontext.Parse accepts, which is the seam between this
	// package and the runner.
	if _, err := runcontext.Parse([]byte(got)); err != nil {
		t.Errorf("the runner would refuse what this package returns: %v", err)
	}
}

// Read on failure too: a step that published and then failed said something
// true up to that point.
func TestPublishedContextIsReadOnFailureAsWell(t *testing.T) {
	pod := podWithTerminated(`{"partial":true}`, 2)
	if pod.PublishedContext() == "" {
		t.Error("a failing step's context was dropped; it is the one clue it left")
	}
}

func TestPublishedContextIsEmptyWhenTheStepWroteNothing(t *testing.T) {
	if got := podWithTerminated("", 0).PublishedContext(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := (Pod{}).PublishedContext(); got != "" {
		t.Errorf("a pod with no status returned %q", got)
	}
}

func podWithTerminated(message string, exit int) Pod {
	raw := `{"status":{"containerStatuses":[{"name":"` + containerName +
		`","state":{"terminated":{"exitCode":` + itoa(exit) +
		`,"reason":"Completed","message":` + quote(message) + `}}}]}}`
	var p Pod
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		panic(err)
	}
	return p
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return strings.TrimSpace(string(rune('0' + n)))
}
