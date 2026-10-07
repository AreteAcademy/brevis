package memcached_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/metastore/memcached"
)

// stalled answers the handshake and nothing else.
//
// `Open` pings before it returns, so a listener that says nothing at all
// fails at the wrong step and proves nothing about the calls that follow.
// This one gets past the door and then goes quiet, which is what a backend
// under load looks like.
func stalled(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(line, "version") {
						_, _ = c.Write([]byte("VERSION 1.6.45\r\n"))
						continue
					}
					// Everything else: hold the connection and answer
					// nothing. No close, because a close is an error the
					// client reports immediately -- silence is the case
					// that used to hang.
					select {}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// Every call is bounded, because every one of them can run under the buffer's
// lock.
//
// `gomemcache.Add` takes no context, so this store discards the one it is
// handed -- Redis honours it, this cannot. Before this was bounded, the only
// limit was the client's own 5s socket timeout, and `claimTimeout` was 500ms
// of pure decoration: measured at a 2s backend, a Claim with a 500ms context
// returned in 2s with no error, holding the pipe's mutex for all of it.
//
// Claim first because it is the one on the hot path, and then the rest,
// because a cache lookup that hangs for five seconds is the same failure on a
// colder path.
func TestEveryCallIsBoundedByTheClaimBudget(t *testing.T) {
	addr := stalled(t)
	m, err := memcached.Open(context.Background(), addr)
	if err != nil {
		t.Fatalf("Open could not get past the handshake: %v", err)
	}

	ctx := context.Background()
	// Generous: the point is that it returns at all, not that it returns to
	// the millisecond. Anything near the client's old 5s default fails.
	limit := 3 * gateway.ClaimTimeout

	for _, c := range []struct {
		name string
		call func() error
	}{
		{"Claim", func() error { _, err := m.Claim(ctx, "k", time.Minute); return err }},
		{"Get", func() error { _, _, err := m.Get(ctx, "k"); return err }},
		{"Put", func() error { return m.Put(ctx, "k", "v", time.Minute) }},
		{"Incr", func() error { _, err := m.Incr(ctx, "k", time.Minute); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan error, 1)
			t0 := time.Now()
			go func() { done <- c.call() }()

			select {
			case err := <-done:
				took := time.Since(t0)
				if took > limit {
					t.Errorf("%s returned after %s against a stalled backend, "+
						"and it runs under the pipe's lock: every request for "+
						"this stream waited that long", c.name, took.Round(time.Millisecond))
				}
				if err == nil {
					t.Errorf("%s reported success against a backend that "+
						"answered nothing", c.name)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s never returned: the socket has no bound at all", c.name)
			}
		})
	}
}

// slowly answers `add` after a delay, correctly enough for the real client.
//
// Deliberate: the pool is what this exercises, so the server has to be able
// to serve several connections at once and be slow on each.
func slowly(t *testing.T, delay time.Duration) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var dialled atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dialled.Add(1)
			go func() {
				defer func() { _ = c.Close() }()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					f := strings.Fields(line)
					switch {
					case len(f) > 0 && f[0] == "version":
						_, _ = c.Write([]byte("VERSION 1.6.45\r\n"))
					case len(f) == 5 && f[0] == "add":
						// The data block follows: <bytes> plus CRLF.
						n, _ := strconv.Atoi(f[4])
						if _, err := io.ReadFull(r, make([]byte, n+2)); err != nil {
							return
						}
						time.Sleep(delay)
						_, _ = c.Write([]byte("STORED\r\n"))
					default:
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), &dialled
}

// The pool has to allow the parallel claims the trigger now asks for.
//
// The flush trigger is per routing key, so one window claims one key per
// table. The client's default is TWO idle connections, shared by every claim
// on every stream -- with a 500ms socket bound, a backend at 200ms serialises
// those claims two at a time and most of them time out rather than queue.
//
// This pins that nothing in this store serialises them, which is the
// property the fan-out is about to depend on.
func TestTheClaimsCanRunInParallel(t *testing.T) {
	const (
		answer = 200 * time.Millisecond
		claims = 32
	)
	addr, _ := slowly(t, answer)
	m, err := memcached.Open(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, claims)
	t0 := time.Now()
	for i := range claims {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.Claim(context.Background(), fmt.Sprintf("k%d", i), time.Minute)
		}(i)
	}
	wg.Wait()
	took := time.Since(t0)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("claim %d of %d failed after %s: %v -- with two idle "+
				"connections these queue behind each other and the 500ms "+
				"socket bound turns the queue into failures",
				i, claims, took.Round(time.Millisecond), err)
		}
	}
	if took > 4*answer {
		t.Errorf("%d claims took %s against a %s backend: they are queueing on "+
			"connections rather than running", claims, took.Round(time.Millisecond), answer)
	}
}

// The pool keeps the connections a window opens, instead of dialling them
// again next window.
//
// `MaxIdleConns` does NOT limit how many run at once -- the client dials a
// new connection whenever none is free, so parallelism was never the
// question, and a first version of this file claimed it was. What the
// setting bounds is how many it KEEPS: at the library default of two, a
// window of claims dials one connection per table, uses it, and closes all
// but two. The next window dials them all again.
//
// One claim per table per window, several streams, several replicas: that is
// a handshake per table per window, against a backend whose whole appeal is
// that it is cheap.
func TestThePoolSurvivesTheWindow(t *testing.T) {
	const (
		waves  = 5
		claims = 16
	)
	addr, dialled := slowly(t, time.Millisecond)
	m, err := memcached.Open(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}

	for w := range waves {
		var wg sync.WaitGroup
		for i := range claims {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _ = m.Claim(context.Background(), fmt.Sprintf("w%d_k%d", w, i), time.Minute)
			}(i)
		}
		wg.Wait()
	}

	// One wave's worth of connections, plus the handshake's, plus slack for
	// the races a pool has by nature. Re-dialling every wave would be five
	// times this.
	if n := dialled.Load(); n > claims*2 {
		t.Errorf("%d connections dialled for %d waves of %d claims: the pool "+
			"is not keeping them, so every window pays a handshake per table",
			n, waves, claims)
	}
}
