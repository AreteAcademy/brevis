package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution"
	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// A pool of agents behind one address, which is what "one Deployment with an
// HPA" is.
//
// The agent is stateful per execution -- `runs map[string]*execution`, with a
// ring per entry -- so Resume and Cancel only mean anything at the instance
// that is running the step. Behind a Service the engine reaches whichever
// replica answers, and the step's own protocol breaks on the first network
// blip: the executor resumes, lands somewhere else, is told "not running
// here", and FAILS A STEP THAT IS STILL RUNNING.
//
// That is the worst shape available: the stream holds in every test anybody
// runs by hand, and it breaks in production under exactly the conditions
// resume exists for.

// balancer is one address in front of N agents, as a Service is.
//
// It round-robins, and it can cut the first response mid-stream -- which is
// not cruelty, it is the blip. Nothing else in this test has to go wrong.
type balancer struct {
	upstreams []string
	next      atomic.Int64
	cutAfter  int // bytes of the FIRST response to forward before hanging up
	cut       atomic.Bool
	seen      atomic.Int64
}

func (b *balancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.seen.Add(1)
	target := b.upstreams[int(b.next.Add(1)-1)%len(b.upstreams)]

	u, _ := url.Parse(target + r.URL.Path)
	u.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)

	buf := make([]byte, 64)
	forwarded := 0
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
			forwarded += n
			// The blip, once.
			if b.cutAfter > 0 && forwarded >= b.cutAfter && b.cut.CompareAndSwap(false, true) {
				panic(http.ErrAbortHandler)
			}
		}
		if rerr != nil {
			return
		}
	}
}

// silent turns the advertisement off on every agent of a pool, which is what
// a deployment that forgot the downward API looks like.
func silent(t *testing.T, e *remote.Executor) {
	t.Helper()
	for _, a := range agentsOf[e] {
		a.Advertise("")
	}
}

// agentsOf remembers which agents belong to which executor, so a test can
// reach them after poolOf built them.
var agentsOf = map[*remote.Executor][]*agent.Agent{}

// poolOf starts n real agents and one address in front of them.
func poolOf(t *testing.T, n, cutAfter int) (*remote.Executor, *balancer) {
	t.Helper()
	b := &balancer{cutAfter: cutAfter}
	var agents []*agent.Agent
	for range n {
		// Each replica is TOLD its own address, which is what the deployment
		// does with the downward API: `--advertise
		// https://$(POD_NAME).dbt.svc:9443`. The server has to exist before the
		// agent can be told, so the handler is wired after.
		var opt agent.Options
		opt.RingSize = 1000
		opt.Shell = []string{"/bin/sh", "-c"}
		a := agent.New(opt)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.Handler(quiet()).ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		a.Advertise(srv.URL)
		b.upstreams = append(b.upstreams, srv.URL)
		agents = append(agents, a)
	}
	front := httptest.NewServer(b)
	t.Cleanup(front.Close)

	e := &remote.Executor{
		Host:       "pool",
		Agent:      remote.HTTPAgent{BaseURL: front.URL},
		LeaseLimit: 5 * time.Second,
	}
	agentsOf[e] = agents
	t.Cleanup(func() { delete(agentsOf, e) })
	return e, b
}

// A blip against a pool must not fail a step that is still running.
//
// The step prints for a couple of seconds; the balancer hangs up after the
// first few lines. The engine resumes, and the resume has to reach the
// instance that owns the execution -- not whichever replica the address
// happens to answer with.
func TestABlipAgainstAPoolResumesAtTheRightInstance(t *testing.T) {
	e, b := poolOf(t, 2, 80)

	events := run(t, e, task("pool-1",
		`for i in 1 2 3 4 5 6 7 8; do echo line-$i; sleep 0.15; done`))

	if got := last(events); got.Kind != execution.EventSucceeded {
		t.Fatalf("the step ended %s: %v\n\nThe balancer hung up once, which is "+
			"a blip and not a failure. The resume reached a different replica, "+
			"which has never heard of this execution -- and the process is "+
			"still running on the one that does.", got.Kind, got.Err)
	}

	// Every line survived the blip, which is what the ring is for.
	all := strings.Join(logs(events), "\n")
	for i := 1; i <= 8; i++ {
		if !strings.Contains(all, "line-"+itoa(i)) {
			t.Errorf("line-%d is missing after the resume:\n%s", i, all)
		}
	}
	// The blip has to have happened, or this test proves nothing.
	if !b.cut.Load() {
		t.Fatal("the balancer never hung up: the step finished before the cut, " +
			"so no resume was exercised and this test is green for the wrong reason")
	}

	// And the resume did NOT go back through the shared address -- it went
	// straight to the instance. One request through the front door is the
	// whole mechanism, stated as a number.
	if b.seen.Load() != 1 {
		t.Errorf("the balancer saw %d requests, want 1: a resume that goes "+
			"through the shared address is a resume that can land anywhere",
			b.seen.Load())
	}
}

// And without the advertisement the same pool fails, which is what says the
// mechanism is what fixed it.
//
// Not decoration: everything else in the setup is identical, so a green test
// above with this one also green would mean the blip, the ring or the balancer
// was doing the work.
func TestWithoutAnAdvertisementTheSamePoolStillBreaks(t *testing.T) {
	e, b := poolOf(t, 2, 80)
	silent(t, e)

	events := run(t, e, task("pool-2",
		`for i in 1 2 3 4 5 6 7 8; do echo line-$i; sleep 0.15; done`))

	if !b.cut.Load() {
		t.Fatal("the balancer never hung up; nothing was exercised")
	}
	got := last(events)
	if got.Kind == execution.EventSucceeded {
		t.Fatal("a pool with no advertisement survived a blip. Either the " +
			"engine stopped needing the instance, or this test stopped " +
			"creating the condition -- and the second is the one that rots")
	}
	if !strings.Contains(got.Err.Error(), "not running here") {
		t.Errorf("the failure is %v, and the one this is about is the agent "+
			"saying the execution is not running here", got.Err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// Cancel reaches the instance that is running the step, and this is the half
// with the worse failure.
//
// A resume that lands on the wrong replica at least SAYS so -- the step is
// failed, loudly and wrongly. Cancel is answered by whichever replica picks
// up: it looks the execution up, does not find it, falls back to a state file
// in its OWN directory, finds nothing there either, and returns success. The
// engine reports the step stopped and the process keeps running.
//
// The marker is what tells the two apart. Killing the shell alone leaves the
// child to write it; cancelling the wrong replica leaves BOTH alive.
func TestCancelOnAPoolReachesTheInstanceThatIsRunning(t *testing.T) {
	e, _ := poolOf(t, 2, 0)

	marker := filepath.Join(t.TempDir(), "still-alive")
	tk := task("pool-3", fmt.Sprintf(`(sleep 1; touch %q) & sleep 5`, marker))

	ch, err := e.Execute(context.Background(), tk)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := e.Cancel(context.Background(), "pool-3"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	collect(t, ch)

	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the step outlived the cancel. With two replicas behind one " +
			"address, a cancel that does not address the instance is answered " +
			"by whichever one picks up -- and it answers SUCCESS, because an " +
			"execution it never had is an execution it cannot find running")
	}
}
