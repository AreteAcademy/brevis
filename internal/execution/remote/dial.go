package remote

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"
)

// Replicas is how the engine reaches a host that is a POOL of agents behind
// one name (#146).
//
// The name is a Service's, and it resolves to every pod of the pool, a
// draining one included -- it has to, because the engine comes back to a
// draining agent to resume the step it handed over (publishNotReadyAddresses,
// #136). What the name cannot say is which of them takes new work. So the
// engine dials the addresses itself, and after one refuses a start for
// draining, it goes to the others first until drainingSkip has passed.
//
// One per host, shared by every copy of its HTTPAgent: it holds the
// connections and what the addresses last answered.
type Replicas struct {
	// Lookup resolves a host to the addresses (host:port) to dial. Nil takes
	// the system resolver. A test passes a map.
	Lookup func(ctx context.Context, host, port string) ([]string, error)

	// TLSClientConfig is for an agent behind a certificate the system does not
	// trust. Nil is the system's roots, as without Replicas.
	TLSClientConfig *tls.Config

	once      sync.Once
	transport *http.Transport

	mu      sync.Mutex
	refused map[string]time.Time // address -> when it refused a start for draining

	// Seams for the test of the order; nil is time.Now and a random shuffle.
	now     func() time.Time
	shuffle func([]string)
}

// drainingSkip is how long an address that refused a start for draining is
// tried last. Long enough that the starts of a burst all go elsewhere; short
// against a drain, so a replica that came back -- or a new pod given the same
// address -- is not left out for long. It is an order, not an exclusion: with
// every address skipped, they are all still tried.
const drainingSkip = 30 * time.Second

func (r *Replicas) client() *http.Client {
	r.once.Do(func() {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = r.dial
		if r.TLSClientConfig != nil {
			t.TLSClientConfig = r.TLSClientConfig.Clone()
		}
		// HTTP/1.1 ONLY, and it is what makes the rest work. Over HTTP/2 every
		// start rides the one connection already open to an agent, and while
		// that agent streams a step the connection is never idle, so never
		// closed, so never dialed again: the order below would never get to
		// choose, and every start would go back to the agent that refused it.
		// The cost is a connection per step that is streaming, which is a
		// handful.
		t.ForceAttemptHTTP2 = false
		t.Protocols = new(http.Protocols)
		t.Protocols.SetHTTP1(true)
		r.transport = t
	})
	// No overall timeout, for the reason HTTPAgent.client gives.
	return &http.Client{Transport: r.transport}
}

// dial connects to one of the addresses the host resolves to.
func (r *Replicas) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := r.lookup(ctx, host, port)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s resolves to no address", host)
	}
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	var last error
	for _, a := range r.order(addrs) {
		c, err := d.DialContext(ctx, network, a)
		if err == nil {
			return c, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

// order is the addresses to try, in turn: shuffled, so a pool shares the
// starts, with the ones that refused for draining within drainingSkip last.
func (r *Replicas) order(addrs []string) []string {
	at := r.clock()

	var fresh, skipped []string
	r.mu.Lock()
	for _, a := range addrs {
		when, ok := r.refused[a]
		switch {
		case ok && at.Sub(when) < drainingSkip:
			skipped = append(skipped, a)
		case ok:
			delete(r.refused, a)
			fallthrough
		default:
			fresh = append(fresh, a)
		}
	}
	r.mu.Unlock()

	shuffle := r.shuffle
	if shuffle == nil {
		shuffle = func(s []string) { rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] }) }
	}
	shuffle(fresh)
	shuffle(skipped)
	return append(fresh, skipped...)
}

// drained records that the agent at `addr` refused a start for draining.
//
// And closes the idle connections, the one to that agent among them: an agent
// from 0.38 does not close it itself, and the next start would be sent down it,
// to the same agent, without any dial for the order to steer.
func (r *Replicas) drained(addr string) {
	if addr == "" {
		return
	}
	at := r.clock()
	r.mu.Lock()
	if r.refused == nil {
		r.refused = map[string]time.Time{}
	}
	r.refused[addr] = at
	r.mu.Unlock()
	if r.transport != nil {
		r.transport.CloseIdleConnections()
	}
}

func (r *Replicas) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Replicas) lookup(ctx context.Context, host, port string) ([]string, error) {
	if r.Lookup != nil {
		return r.Lookup(ctx, host, port)
	}
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	addrs := make([]string, len(ips))
	for i, ip := range ips {
		addrs[i] = net.JoinHostPort(ip, port)
	}
	return addrs, nil
}
