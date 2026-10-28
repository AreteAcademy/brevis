package remote_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// replica is one agent of a pool, as the dial tests need it (#146).
//
// Once draining it refuses a start the way a 0.38 agent did -- 503 and the
// message, no Brevis-Refusal, no Connection: close -- because that is the case
// where nothing but the engine keeps the next start away from it. A healthy
// one answers a start with a stream that stays open while `streaming` is set:
// a step still running, the way the agent in the issue was.
type replica struct {
	srv       *httptest.Server
	draining  atomic.Bool
	streaming bool
	starts    atomic.Int32
	others    atomic.Int32 // resumes and cancels
}

func newReplica(t *testing.T, tls, streaming bool) *replica {
	t.Helper()
	r := &replica{streaming: streaming}
	running := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/exec" {
			r.others.Add(1)
			w.Header().Set("Content-Type", "application/x-ndjson")
			return
		}
		r.starts.Add(1)
		if r.draining.Load() {
			http.Error(w, "this agent is draining: it finishes the steps it has and takes no new one",
				http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		if r.streaming {
			w.(http.Flusher).Flush()
			select {
			case <-running:
			case <-req.Context().Done():
			}
		}
	})
	if tls {
		r.srv = httptest.NewUnstartedServer(h)
		r.srv.EnableHTTP2 = true
		r.srv.StartTLS()
	} else {
		r.srv = httptest.NewServer(h)
	}
	// LIFO: the running streams end before the server is closed under them.
	t.Cleanup(r.srv.Close)
	t.Cleanup(func() { close(running) })
	return r
}

func (r *replica) addr() string { return r.srv.Listener.Addr().String() }

// names is the DNS the tests control: a Service name resolves to its pods.
type names struct {
	mu sync.Mutex
	m  map[string][]string
}

func (n *names) set(host string, addrs ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.m == nil {
		n.m = map[string][]string{}
	}
	n.m[host] = addrs
}

func (n *names) lookup(_ context.Context, host, _ string) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.m[host], nil
}

func start(t *testing.T, a remote.HTTPAgent) error {
	t.Helper()
	s, err := a.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "e", Command: "true"})
	if err == nil {
		_ = s.Stream.Close()
	}
	return err
}

// The issue: a pool of two behind one name, and the engine already holding a
// kept-alive connection to the one that then begins draining. Every start
// after that lands on the other one, and the draining one is asked at most
// once -- the refusal that told the engine to look elsewhere.
func TestAStartRefusedForDrainingGoesToAnotherReplica(t *testing.T) {
	a, b := newReplica(t, false, false), newReplica(t, false, false)
	var dns names
	dns.set("pool.test", a.addr())

	agent := remote.HTTPAgent{BaseURL: "http://pool.test", StartBackoff: fast,
		Replicas: &remote.Replicas{Lookup: dns.lookup}}
	if err := start(t, agent); err != nil {
		t.Fatalf("the first start: %v", err)
	}

	a.draining.Store(true)
	dns.set("pool.test", a.addr(), b.addr())
	for i := range 10 {
		if err := start(t, agent); err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
	}
	if got := a.starts.Load() - 1; got > 1 {
		t.Errorf("the draining replica was sent %d new starts, wanted at most 1", got)
	}
	if got := b.starts.Load(); got != 10 {
		t.Errorf("the healthy replica ran %d starts, wanted 10", got)
	}
}

// The same over TLS, with the replica still streaming a step it took before
// it began draining. HTTP/2 would carry every start down that one connection
// -- busy, so never idle, so never closed -- and no new dial would ever get
// to choose. The name is example.com because it is what httptest's
// certificate is for: the server is checked against the NAME, not the
// address it was dialed at.
func TestOverTLSAReplicaStillStreamingIsNotAskedAgain(t *testing.T) {
	a, b := newReplica(t, true, true), newReplica(t, true, false)
	var dns names
	dns.set("example.com", a.addr())

	agent := remote.HTTPAgent{BaseURL: "https://example.com", StartBackoff: fast,
		Replicas: &remote.Replicas{Lookup: dns.lookup,
			TLSClientConfig: a.srv.Client().Transport.(*http.Transport).TLSClientConfig}}

	running, err := agent.Start(context.Background(), remote.StartRequest{
		Protocol: remote.Protocol, ExecutionID: "long", Command: "sleep 1h"})
	if err != nil {
		t.Fatalf("the first start: %v", err)
	}
	defer func() { _ = running.Stream.Close() }()

	a.draining.Store(true)
	dns.set("example.com", a.addr(), b.addr())
	for i := range 5 {
		if err := start(t, agent); err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
	}
	if got := a.starts.Load() - 1; got > 1 {
		t.Errorf("the draining replica was sent %d new starts, wanted at most 1", got)
	}
}

// Nobody to go to: the refusal is what Start answers, after its tries.
func TestEveryReplicaDrainingIsTheDrainingRefusal(t *testing.T) {
	a, b := newReplica(t, false, false), newReplica(t, false, false)
	a.draining.Store(true)
	b.draining.Store(true)
	var dns names
	dns.set("pool.test", a.addr(), b.addr())

	agent := remote.HTTPAgent{BaseURL: "http://pool.test", StartBackoff: fast,
		Replicas: &remote.Replicas{Lookup: dns.lookup}}
	if err := start(t, agent); !errors.Is(err, remote.ErrAgentDraining) {
		t.Fatalf("Start = %v, wanted ErrAgentDraining", err)
	}
	if got := a.starts.Load() + b.starts.Load(); got != 5 {
		t.Errorf("%d starts sent, wanted 5", got)
	}
}

// A resume or a cancel is for the agent that HOLDS the execution, draining or
// not -- a draining agent is exactly the one that must answer them. Steering
// away from it is for starts only.
func TestResumeAndCancelReachTheReplicaThatRefusedAStart(t *testing.T) {
	a := newReplica(t, false, false)
	a.draining.Store(true)
	var dns names
	dns.set("pool.test", a.addr())
	dns.set("a.pool.test", a.addr())

	agent := remote.HTTPAgent{BaseURL: "http://pool.test", StartBackoff: fast,
		Replicas: &remote.Replicas{Lookup: dns.lookup}}
	if err := start(t, agent); !errors.Is(err, remote.ErrAgentDraining) {
		t.Fatalf("Start = %v, wanted ErrAgentDraining", err)
	}

	stream, err := agent.Resume(context.Background(), "http://a.pool.test", "e",
		remote.Resume{Protocol: remote.Protocol})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	_ = stream.Close()
	if err := agent.Cancel(context.Background(), "http://a.pool.test", "e"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := a.others.Load(); got != 2 {
		t.Errorf("the replica holding the execution got %d of the resume and the cancel, wanted 2", got)
	}
}
