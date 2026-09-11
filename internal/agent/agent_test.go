package agent_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// The test that had to exist.
//
// Everything before this proved the ENGINE against a fake agent and the AGENT
// against nothing: two programs, each satisfying a document. This puts the real
// executor and the real agent on a real socket, which is the only thing that
// proves they speak to each other at all.
//
// It is in-process, so it costs a few milliseconds -- there is no reason for a
// contract between two halves of one repository to be checked by hand.

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// pair wires a real Executor to a real Agent over HTTP.
func pair(t *testing.T, opt agent.Options) *remote.Executor {
	t.Helper()
	if opt.Shell == nil {
		opt.Shell = []string{"/bin/sh", "-c"}
	}
	a := agent.New(opt)
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	return &remote.Executor{
		Host:       "test-host",
		Agent:      remote.HTTPAgent{BaseURL: srv.URL, Token: opt.Token},
		LeaseLimit: 5 * time.Second,
	}
}

func task(id, command string) execution.TaskExec {
	return execution.TaskExec{
		ExecutionID: id, NodeID: "step", Workflow: "vendas",
		RunID: "run-1", Command: command,
	}
}

func collect(t *testing.T, ch <-chan execution.Event) []execution.Event {
	t.Helper()
	var out []execution.Event
	deadline := time.After(20 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-deadline:
			t.Fatalf("the stream never ended; %d event(s) so far", len(out))
			return out
		}
	}
}

func run(t *testing.T, e *remote.Executor, tk execution.TaskExec) []execution.Event {
	t.Helper()
	ch, err := e.Execute(context.Background(), tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return collect(t, ch)
}

func logs(events []execution.Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind == execution.EventLog {
			out = append(out, e.Stream+": "+e.Message)
		}
	}
	return out
}

func last(events []execution.Event) execution.Event { return events[len(events)-1] }

// A step runs on the host and its output reaches the engine, IN ORDER WITHIN
// EACH STREAM.
//
// Within a stream and not across the two, and the distinction is the system's
// rather than this test's convenience. stdout and stderr are separate pipes
// with separate buffers; nothing -- not the OS, not the shell -- promises that
// a line written to one appears before a line written later to the other. The
// local executor forwards them on two goroutines for the same reason and has
// always had the same property.
//
// This test asserted the interleaving at first and failed, which is the right
// outcome for an expectation the system cannot honour. What IS guaranteed is
// worth pinning, so it is what is checked.
func TestAStepRunsAndItsOutputComesBackInOrderWithinEachStream(t *testing.T) {
	e := pair(t, agent.Options{})
	got := run(t, e, task("x1", `echo one; echo two >&2; echo three`))

	if got[0].Kind != execution.EventStarted {
		t.Errorf("first event is %v", got[0].Kind)
	}
	if last(got).Kind != execution.EventSucceeded {
		t.Fatalf("the step did not succeed: %+v", last(got))
	}

	byStream := map[string][]string{}
	for _, ev := range got {
		if ev.Kind == execution.EventLog {
			byStream[ev.Stream] = append(byStream[ev.Stream], ev.Message)
		}
	}
	if strings.Join(byStream["stdout"], "|") != "one|three" {
		t.Errorf("stdout = %v, wanted [one three]", byStream["stdout"])
	}
	if strings.Join(byStream["stderr"], "|") != "two" {
		t.Errorf("stderr = %v, wanted [two]", byStream["stderr"])
	}
}

// stdout and stderr stay apart, which the POD executor cannot do at all --
// Kubernetes merges them. A remote host is better informed and must not throw
// that away.
func TestTheTwoStreamsStayApart(t *testing.T) {
	e := pair(t, agent.Options{})
	got := run(t, e, task("x2", `echo out; echo err >&2`))

	var outs, errs int
	for _, ev := range got {
		switch ev.Stream {
		case "stdout":
			outs++
		case "stderr":
			errs++
		}
	}
	if outs != 1 || errs != 1 {
		t.Errorf("stdout=%d stderr=%d; the streams were merged", outs, errs)
	}
}

// A non-zero exit reaches the engine as a failure carrying the code.
func TestANonZeroExitCrossesAsItsCode(t *testing.T) {
	e := pair(t, agent.Options{})
	got := run(t, e, task("x3", `echo before; exit 3`))

	if last(got).Kind != execution.EventFailed || last(got).ExitCode != 3 {
		t.Errorf("last event: %+v", last(got))
	}
	if len(logs(got)) != 1 {
		t.Errorf("the output before the failure was lost: %v", logs(got))
	}
}

// DECISION 3, end to end and for real: the coordinate crosses, the value does
// not, and the agent resolves it from ITS store under ITS allowlist.
func TestTheAgentResolvesTheSecretAndTheEngineNeverHoldsIt(t *testing.T) {
	store := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store, "vendor-api"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Written with a trailing newline, the way `echo > file` leaves one. A
	// token with a newline on the end is a 401 nobody can see.
	if err := os.WriteFile(filepath.Join(store, "vendor-api", "token"), []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := pair(t, agent.Options{SecretsDir: store, AllowedSecrets: []string{"vendor-api"}})
	tk := task("x4", `echo "token is $VENDOR_TOKEN"`)
	tk.Secrets = map[string]string{"VENDOR_TOKEN": "vendor-api/token"}

	got := run(t, e, tk)
	if last(got).Kind != execution.EventSucceeded {
		t.Fatalf("the step failed: %+v", last(got))
	}
	if want := "stdout: token is s3cr3t"; logs(got)[0] != want {
		t.Errorf("the step saw %q, wanted %q", logs(got)[0], want)
	}
}

// And a secret this host does not allow is a REFUSED STEP naming the
// coordinate -- not a step that runs with the variable silently unset, which is
// how a 401 three layers down gets blamed on the wrong service.
func TestASecretOutsideTheAllowlistRefusesTheStep(t *testing.T) {
	store := t.TempDir()
	_ = os.MkdirAll(filepath.Join(store, "brevis-database"), 0o700)
	_ = os.WriteFile(filepath.Join(store, "brevis-database", "password"), []byte("no"), 0o600)

	// The allowlist names something else entirely.
	e := pair(t, agent.Options{SecretsDir: store, AllowedSecrets: []string{"vendor-api"}})
	tk := task("x5", `echo "$DB_PASSWORD"`)
	tk.Secrets = map[string]string{"DB_PASSWORD": "brevis-database/password"}

	_, err := e.Execute(context.Background(), tk)
	if err == nil {
		t.Fatal("a step asking for a forbidden secret was started")
	}
	for _, want := range []string{"brevis-database", "allow-secrets"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// A secret name that climbs out of the store cannot READ what is out there.
//
// The name arrives from a workflow file somebody else wrote, so this is the one
// place where a string from a file becomes a path on a host this program does
// not own.
//
// The first version of this test put no file outside the store and passed
// because the read found nothing -- proving the file was absent, not that the
// traversal was stopped. A real file is planted next to the store now, and the
// assertion is that its CONTENTS never reach the step.
func TestASecretNameCannotEscapeTheStore(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store")
	if err := os.MkdirAll(filepath.Join(store, "public"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The thing the traversal is reaching for, deliberately readable.
	if err := os.WriteFile(filepath.Join(root, "private"), []byte("SHOULD-NEVER-LEAK"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The allowlist is generous on purpose: this asserts the PATH handling, not
	// the allowlist, which has its own test.
	e := pair(t, agent.Options{
		SecretsDir:     store,
		AllowedSecrets: []string{"..", "../..", "public"},
	})
	tk := task("x6", `echo "value=[$X]"`)
	tk.Secrets = map[string]string{"X": "../private"}

	ch, err := e.Execute(context.Background(), tk)
	if err != nil {
		// Refused before running is a fine outcome: nothing leaked.
		if strings.Contains(err.Error(), "SHOULD-NEVER-LEAK") {
			t.Fatal("the refusal itself carried the file's contents")
		}
		return
	}
	// Started anyway? Then the value must not be there.
	for _, ev := range collect(t, ch) {
		if strings.Contains(ev.Message, "SHOULD-NEVER-LEAK") {
			t.Fatalf("a file outside the store reached the step: %q", ev.Message)
		}
	}
}

// DECISION 4: a step that prints nothing for longer than the lease still keeps
// it, because the agent beats. This is what makes silence mean death rather
// than slowness -- and it is checked against a REAL beat rather than a fake.
func TestAQuietStepKeepsItsLeaseAgainstTheRealAgent(t *testing.T) {
	a := agent.New(agent.Options{AliveEvery: 80 * time.Millisecond})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	e := &remote.Executor{
		Host:  "test-host",
		Agent: remote.HTTPAgent{BaseURL: srv.URL},
		// Shorter than the step, longer than the beat: without the heartbeat
		// this fails, and with it the step survives four times its own lease.
		LeaseLimit: 250 * time.Millisecond,
	}
	got := run(t, e, task("x7", `sleep 1; echo done`))

	if last(got).Kind != execution.EventSucceeded {
		t.Fatalf("a quiet step lost its lease: %+v", last(got))
	}
	// And the heartbeat is not something a person needs in a log.
	for _, l := range logs(got) {
		if strings.Contains(l, "alive") {
			t.Errorf("a heartbeat reached the log: %q", l)
		}
	}
}

// DECISION 2: cancel reaches across the socket and stops the process GROUP --
// not just the shell, which would leave whatever it spawned running.
func TestCancelStopsTheWholeProcessGroup(t *testing.T) {
	e := pair(t, agent.Options{})

	marker := filepath.Join(t.TempDir(), "still-alive")
	// The shell spawns a child that would write the marker a second from now.
	// Killing only the shell leaves the child, and the file appears.
	tk := task("x8", fmt.Sprintf(`(sleep 1; touch %q) & sleep 5`, marker))

	ch, err := e.Execute(context.Background(), tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := e.Cancel(context.Background(), "x8"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	collect(t, ch)

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the child outlived the cancel: only the shell was killed")
	}
}

// DECISION 1: a resume picks the stream up where it stopped, against the real
// ring.
func TestAResumeContinuesFromTheRealRing(t *testing.T) {
	a := agent.New(agent.Options{RingSize: 1000})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)
	client := remote.HTTPAgent{BaseURL: srv.URL}

	stream, err := client.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "x9", NodeID: "step",
		Command: `for i in 1 2 3 4 5 6; do echo line-$i; sleep 0.1; done`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Read a little, then hang up in the middle -- a network blip.
	buf := make([]byte, 120)
	n, _ := stream.Read(buf)
	_ = stream.Close()
	if n == 0 {
		t.Fatal("nothing arrived before the disconnect")
	}

	resumed, err := client.Resume(context.Background(), "x9", remote.Resume{
		Protocol: remote.Protocol, After: 1, Reason: "a test hung up",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()

	rest, err := io.ReadAll(resumed)
	if err != nil {
		t.Fatal(err)
	}
	// Every line of the step's output is in the resumed half, because the
	// process wrote into the ring whether anybody was reading or not.
	for i := 1; i <= 6; i++ {
		if !strings.Contains(string(rest), fmt.Sprintf("line-%d", i)) {
			t.Errorf("line-%d is missing from the resumed stream:\n%s", i, rest)
		}
	}
	// And nothing before the cursor is replayed: a repeated line would be a
	// counter metric added twice.
	if strings.Contains(string(rest), `"seq":1`) {
		t.Errorf("the resume replayed a line the engine already had:\n%s", rest)
	}
}

// A gap the ring cannot cover is a 410 carrying both numbers, which is what the
// engine turns back into a GapError and a failed step.
func TestAResumePastTheRingIsAnHonestGap(t *testing.T) {
	// One line of history, so anything but the newest is already gone.
	a := agent.New(agent.Options{RingSize: 1})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)
	client := remote.HTTPAgent{BaseURL: srv.URL}

	stream, err := client.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "x10", NodeID: "step",
		Command: `for i in $(seq 1 40); do echo line-$i; done; sleep 2`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	time.Sleep(300 * time.Millisecond)

	_, err = client.Resume(context.Background(), "x10", remote.Resume{
		Protocol: remote.Protocol, After: 1,
	})
	var gap remote.GapError
	if !asGap(err, &gap) {
		t.Fatalf("a resume past the ring gave %v, not a GapError", err)
	}
	if gap.Wanted != 2 || gap.Available <= gap.Wanted {
		t.Errorf("the gap does not say what is missing: %+v", gap)
	}
}

// The token is checked, and the engine's refusal says so rather than looking
// like a broken host.
func TestTheWrongTokenIsRefused(t *testing.T) {
	a := agent.New(agent.Options{Token: "right"})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	e := &remote.Executor{Host: "test-host", Agent: remote.HTTPAgent{BaseURL: srv.URL, Token: "wrong"}}
	if _, err := e.Execute(context.Background(), task("x11", "echo hi")); err == nil {
		t.Fatal("a wrong token was accepted")
	}
}

// A protocol the agent does not speak is refused LOUDLY rather than guessed at.
// The two are separate programs on separate release cycles.
func TestAProtocolMismatchIsRefused(t *testing.T) {
	a := agent.New(agent.Options{})
	srv := httptest.NewServer(a.Handler(quiet()))
	t.Cleanup(srv.Close)

	_, err := remote.HTTPAgent{BaseURL: srv.URL}.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol + 99, ExecutionID: "x12", Command: "echo hi",
	})
	if err == nil {
		t.Fatal("a future protocol was accepted")
	}
	if !strings.Contains(err.Error(), "upgrading") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

// asGap is errors.As with the concrete type.
func asGap(err error, target *remote.GapError) bool {
	for err != nil {
		if g, ok := err.(remote.GapError); ok {
			*target = g
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
