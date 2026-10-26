package remote_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// fast keeps the retry tests in milliseconds; the shape is what is tested.
var fast = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}

// answering is an agent that answers each request with the next status, the
// last one repeating, and counts what it was sent.
func answering(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(n.Add(1)) - 1
		status := statuses[min(i, len(statuses)-1)]
		if status != http.StatusOK {
			http.Error(w, "said no", status)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func startOn(ctx context.Context, url string) error {
	a := remote.HTTPAgent{BaseURL: url, StartBackoff: fast}
	s, err := a.Start(ctx, remote.StartRequest{Protocol: remote.Protocol, ExecutionID: "e", Command: "true"})
	if err == nil {
		_ = s.Stream.Close()
	}
	return err
}

// A draining agent answers 503 (#136), and so does a balancer with nobody
// behind it. Neither ran anything, so the start goes again -- to a replica,
// or to the replacement -- rather than failing an attempt over a rollout.
func TestAStartAnswered503IsSentAgain(t *testing.T) {
	srv, n := answering(t, 503, 503, 200)
	if err := startOn(context.Background(), srv.URL); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := n.Load(); got != 3 {
		t.Errorf("%d requests, wanted 3", got)
	}
}

// Anything else is about the step, and sending it again changes nothing.
func TestOnlyA503IsSentAgain(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnauthorized} {
		srv, n := answering(t, status)
		if err := startOn(context.Background(), srv.URL); err == nil {
			t.Errorf("%d: Start succeeded", status)
		}
		if got := n.Load(); got != 1 {
			t.Errorf("%d: %d requests, wanted 1", status, got)
		}
	}
}

// Bounded: an agent that never stops draining is failed after five, with the
// reason it gave.
func TestA503IsSentAgainFiveTimesAtMost(t *testing.T) {
	srv, n := answering(t, 503)
	err := startOn(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Start = %v, wanted the 503", err)
	}
	if got := n.Load(); got != 5 {
		t.Errorf("%d requests, wanted 5", got)
	}
}

// A pod Kubernetes now publishes before it is ready (publishNotReadyAddresses,
// #136) refuses the connection until it binds; that too ran nothing.
func TestARefusedConnectionIsTriedAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // refused, for now

	go func() {
		time.Sleep(20 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
			})}
		t.Cleanup(func() { _ = srv.Close() })
		_ = srv.Serve(ln)
	}()

	a := remote.HTTPAgent{BaseURL: "http://" + addr,
		StartBackoff: []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}}
	s, err := a.Start(context.Background(), remote.StartRequest{Protocol: remote.Protocol, ExecutionID: "e", Command: "true"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = s.Stream.Close()
}

// The wait between tries is a wait the run can end.
func TestTheBackoffEndsWithTheContext(t *testing.T) {
	srv, _ := answering(t, 503)
	a := remote.HTTPAgent{BaseURL: srv.URL, StartBackoff: []time.Duration{time.Minute}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	began := time.Now()
	_, err := a.Start(ctx, remote.StartRequest{Protocol: remote.Protocol, ExecutionID: "e", Command: "true"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Start = %v, wanted the context's end", err)
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Errorf("Start waited %v past its context", waited)
	}
}

// ONLY a start. A resume or a cancel is about an execution THIS agent holds;
// another replica answering would not be an answer, and the engine's own
// reconnect loop already decides about those.
func TestResumeAndCancelAreNotSentAgain(t *testing.T) {
	srv, n := answering(t, 503)
	a := remote.HTTPAgent{BaseURL: srv.URL, StartBackoff: fast}

	if _, err := a.Resume(context.Background(), "", "e", remote.Resume{Protocol: remote.Protocol}); err == nil {
		t.Error("Resume succeeded against a 503")
	}
	if err := a.Cancel(context.Background(), "", "e"); err == nil {
		t.Error("Cancel succeeded against a 503")
	}
	if got := n.Load(); got != 2 {
		t.Errorf("%d requests for one resume and one cancel, wanted 2", got)
	}
}
