package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// These run the BINARY, with a real signal. Everything the drain does is tested
// in internal/agent; what only this can catch is main itself -- the issue
// (#136) was a main that returned while its shutdown goroutine was still
// waiting, and no test of the package below it could have seen that.

// binary is the agent built from this tree, once, by TestMain.
var binary string

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "brevis-agent-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "brevis-agent")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// agentProcess is one running brevis-agent and how it ended.
type agentProcess struct {
	cmd  *exec.Cmd
	url  string
	done chan error
}

func runAgent(t *testing.T, args ...string) *agentProcess {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cmd := exec.Command(binary, append([]string{"--listen", addr, "--insecure-no-token",
		"--work-dir", t.TempDir(), "--log-level", "warn"}, args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &agentProcess{cmd: cmd, url: "http://" + addr, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.done
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(p.url + "/health") //nolint:noctx
		if err == nil {
			_ = resp.Body.Close()
			return p
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the agent never answered /health")
	return nil
}

func (p *agentProcess) start(t *testing.T, id, command string) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: id, NodeID: "step", Command: command,
	})
	resp, err := http.Post(p.url+"/v1/exec", "application/json", bytes.NewReader(raw)) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	return resp
}

// exited waits up to d and returns the exit code, or -1 if it is still running.
func (p *agentProcess) exited(d time.Duration) int {
	select {
	case err := <-p.done:
		p.done <- err // for Cleanup
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return 0
	case <-time.After(d):
		return -1
	}
}

// The issue's repro, step for step: a host: step running, SIGTERM. The agent
// must outlive the signal, serve the step to its end, and only then exit 0.
func TestSIGTERMDrainsTheRunningStepInsteadOfKillingIt(t *testing.T) {
	p := runAgent(t)
	resp := p.start(t, "e2e-1", "sleep 2; echo finished")

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := p.exited(time.Second); code != -1 {
		t.Fatalf("the agent exited (%d) a second after SIGTERM, with its step running", code)
	}

	var sawFinished, sawExit bool
	s := bufio.NewScanner(resp.Body)
	for s.Scan() {
		l, err := remote.Decode(s.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		sawFinished = sawFinished || strings.Contains(l.Message, "finished")
		if l.Kind == remote.KindExit {
			sawExit = l.Code == 0
		}
	}
	if !sawFinished || !sawExit {
		t.Fatalf("the step did not run to its end (output %v, exit 0 %v)", sawFinished, sawExit)
	}

	if code := p.exited(10 * time.Second); code != 0 {
		t.Fatalf("after the drain the agent exited %d, wanted 0 (-1: still running)", code)
	}
}

// Past --drain-timeout the step is cancelled and the agent says so in its exit
// code, so an operator's tooling can tell a clean drain from a cut one.
func TestADrainTimeoutCancelsAndExitsOne(t *testing.T) {
	p := runAgent(t, "--drain-timeout", "1s")
	_ = p.start(t, "e2e-2", "sleep 30").Body.Close()
	time.Sleep(200 * time.Millisecond)

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := p.exited(15 * time.Second); code != 1 {
		t.Fatalf("the agent exited %d, wanted 1 (-1: still running)", code)
	}
}
