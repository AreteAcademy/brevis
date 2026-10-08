package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// A BOUNDED CONNECTION, over a real one. [#44]
//
// Behind a ClusterIP Service, kube-proxy picks a backend once per TCP
// connection and not per request, so a producer with a handful of
// long-lived connections sends everything to one or two replicas however
// many are running -- and a replica the HPA adds receives nothing, because
// nobody dials it. A consumer measured one of six replicas taking about
// half the events with its buffer at 99% of `max_records` while four others
// took under three events a second. The sink kept up the whole time.
//
// Tested over an actual keep-alive connection rather than by calling the
// handler: what is being claimed is that the SERVER ends the connection,
// and a handler returning a header proves nothing about that.
func TestAConnectionPastItsAgeIsClosed(t *testing.T) {
	var served int
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = boundConnections(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served++
			fmt.Fprint(w, "ok")
		}), 40*time.Millisecond)
	ts.Config.ConnContext = stampAccept
	ts.Start()
	defer ts.Close()

	client := ts.Client()

	// Young: keep-alive survives.
	if closed := get(t, client, ts.URL); closed {
		t.Error("a connection younger than the limit was closed")
	}

	time.Sleep(60 * time.Millisecond)

	// Past the limit: the request still succeeds AND the connection ends.
	// Both halves matter -- closing instead of answering would lose the
	// batch, and answering without closing would leave the imbalance.
	if closed := get(t, client, ts.URL); !closed {
		t.Error("a connection past the limit stayed open")
	}
	if served != 2 {
		t.Errorf("the handler ran %d times; every request has to be served", served)
	}
}

// OFF IS OFF, and it is the default. `max_conn_age` is policy -- how long a
// client may keep one connection -- so it is set by whoever operates the
// deployment, not by this.
func TestWithNoAgeNothingIsClosed(t *testing.T) {
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = boundConnections(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "ok")
		}), 0)
	ts.Config.ConnContext = stampAccept
	ts.Start()
	defer ts.Close()

	client := ts.Client()
	get(t, client, ts.URL)
	time.Sleep(30 * time.Millisecond)
	if closed := get(t, client, ts.URL); closed {
		t.Error("a connection was closed with no max_conn_age set")
	}
}

// A SERVER WITHOUT ConnContext MUST NOT CLOSE EVERYTHING. Without the stamp
// there is no accept time, and treating a missing one as "infinitely old"
// would turn every response into a close -- a handshake per request, which
// is the thing the issue's own alternatives section rejects.
func TestWithoutTheStampNothingIsClosed(t *testing.T) {
	ts := httptest.NewUnstartedServer(boundConnections(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "ok")
		}), time.Nanosecond))
	// No ConnContext on purpose.
	ts.Start()
	defer ts.Close()

	if closed := get(t, ts.Client(), ts.URL); closed {
		t.Error("a connection with no accept time was closed; every request " +
			"would pay a handshake")
	}
}

// get makes one request and says whether the server ended the connection.
func get(t *testing.T, c *http.Client, url string) bool {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	// `Close` is what net/http sets when the response carried
	// `Connection: close`, read off the wire rather than off our own header
	// map -- which is the difference between asserting the server's
	// behaviour and asserting our own.
	return resp.Close || strings.EqualFold(resp.Header.Get("Connection"), "close")
}

// --- the config half (issue #44) -------------------------------------------

// `idle_timeout` HAS A DEFAULT and `max_conn_age` DOES NOT, and they are not
// the same kind of knob.
//
// Serving with no idle timeout at all is a DEFECT rather than a policy: Go
// falls back to ReadTimeout when IdleTimeout is zero, and both were zero
// here -- so a connection opened on the first request of the day was still
// eligible at midnight, and the only thing that ever ended one was the
// client or a restart. A file descriptor per abandoned client. A default is
// a correction.
//
// How long a client may KEEP a connection is policy, and policy belongs to
// whoever operates the deployment. So that one stays off.
func TestTheTwoTimeoutsDefaultDifferently(t *testing.T) {
	var c Config
	if got := c.IdleTimeout(); got <= 0 {
		t.Errorf("idle_timeout defaults to %s; serving with none is a file "+
			"descriptor per abandoned client until the process restarts", got)
	}
	if got := c.MaxConnAge(); got != 0 {
		t.Errorf("max_conn_age defaults to %s, and it is policy: off until "+
			"somebody sets it", got)
	}
}

func TestWhatTheFileSaysWins(t *testing.T) {
	c := Config{Listen: Listen{IdleTimeout: 30 * time.Second, MaxConnAge: 2 * time.Minute}}
	if got := c.IdleTimeout(); got != 30*time.Second {
		t.Errorf("idle_timeout is %s and the file said 30s", got)
	}
	if got := c.MaxConnAge(); got != 2*time.Minute {
		t.Errorf("max_conn_age is %s and the file said 2m", got)
	}
}

// A NEGATIVE IS REFUSED BY NAME rather than taken as "off". Reading -1s as
// a disable would be a second way to say something the file has no word
// for, and the author who wrote it meant a duration.
func TestANegativeDurationIsRefused(t *testing.T) {
	for _, c := range []Config{
		{Listen: Listen{IdleTimeout: -time.Second}},
		{Listen: Listen{MaxConnAge: -time.Second}},
	} {
		err := c.checkListenTimeouts()
		if err == nil {
			t.Errorf("%+v was accepted", c.Listen)
			continue
		}
		if !strings.Contains(err.Error(), "listen.") {
			t.Errorf("the refusal does not name the field: %v", err)
		}
	}
}

// THE TWO ARE NOT ORDERED. The first version of this refused an age below
// the idle timeout, on the reasoning that the idle close would come first
// and the age would never fire. It does not follow -- a connection reaches
// the age only by being USED, and one used often enough to stay alive is
// exactly the long-lived producer the age exists to rebalance.
//
// It also refused the example in the issue that asked for the feature:
// `max_conn_age: 60s` with `idle_timeout: 90s`. This is that example.
func TestTheIssuesOwnExampleIsAccepted(t *testing.T) {
	c := Config{Listen: Listen{MaxConnAge: 60 * time.Second, IdleTimeout: 90 * time.Second}}
	if err := c.checkListenTimeouts(); err != nil {
		t.Errorf("the configuration the issue proposed was refused: %v", err)
	}
}

func TestTheOrdinaryCaseIsAccepted(t *testing.T) {
	for _, c := range []Config{
		{},
		{Listen: Listen{IdleTimeout: 90 * time.Second}},
		{Listen: Listen{MaxConnAge: 5 * time.Minute}},
		{Listen: Listen{IdleTimeout: 90 * time.Second, MaxConnAge: 5 * time.Minute}},
		// Either order.
		{Listen: Listen{IdleTimeout: 5 * time.Minute, MaxConnAge: 90 * time.Second}},
	} {
		if err := c.checkListenTimeouts(); err != nil {
			t.Errorf("%+v: %v", c.Listen, err)
		}
	}
}

// THE WIRE FROM THE FILE TO THE REFUSAL. checkListenTimeouts is a method
// nobody has to call, and the tests above pass whether or not Load does --
// which is how a validation ships and never runs.
func TestLoadRefusesTheTimeoutsItShould(t *testing.T) {
	const base = `
name: g
listen:
  addr: :8080
  auth: {type: bearer, keys_from: K}
streams:
  - name: s
    path: /s
    format: json
    identity: {provider: p, entity: e, source_key: id, record_ts: ts}
    sink: {type: files, path: ./out/}
    dead_letter: {type: files, path: ./dl/}
`
	for _, c := range []struct {
		name, extra, want string
	}{
		{"a negative idle timeout", "  idle_timeout: -1s\n", "listen.idle_timeout"},
		{"a negative age", "  max_conn_age: -1s\n", "listen.max_conn_age"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The extra lines belong to `listen:`, which is where they sit
			// in the file; inserting after `addr` keeps the indentation.
			yaml := strings.Replace(base, "  addr: :8080\n", "  addr: :8080\n"+c.extra, 1)
			path := t.TempDir() + "/g.yaml"
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("K", "k")
			_, err := Load(path)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal does not name %s: %v", c.want, err)
			}
		})
	}
}

// BOTH LISTENERS, AND IT IS THE WIRE THAT MATTERS. Everything above tests a
// wrapper and a method; nothing yet says main.go builds its servers with
// them, and a timeout nobody passes to an http.Server is a field in a
// struct.
//
// The metrics listener counts too: the report named `main.go:129` beside
// the ingest one, and it had the same two zeros.
func TestBothServersCarryTheTimeouts(t *testing.T) {
	cfg := &Config{Listen: Listen{Addr: ":0", MaxConnAge: 2 * time.Minute}}

	for _, c := range []struct {
		name string
		s    *http.Server
	}{
		{"ingest", listener(cfg, ":0", nil)},
		{"metrics", listener(cfg, ":0", nil)},
	} {
		if c.s.IdleTimeout != cfg.IdleTimeout() {
			t.Errorf("%s: IdleTimeout is %s and the config says %s",
				c.name, c.s.IdleTimeout, cfg.IdleTimeout())
		}
		if c.s.ReadHeaderTimeout <= 0 {
			t.Errorf("%s: no ReadHeaderTimeout", c.name)
		}
		if c.s.ConnContext == nil {
			t.Errorf("%s: no ConnContext, so no connection has an accept "+
				"time and max_conn_age can never fire", c.name)
		}
	}
}

// AND THE AGE WRAPS ONLY THE INGEST HANDLER. Recycling a Prometheus
// scraper's connection buys nothing -- there is one of it, it is not behind
// the Service doing the balancing, and the cost would be a handshake per
// scrape for ever.
func TestTheAgeDoesNotWrapTheScrapeEndpoint(t *testing.T) {
	cfg := &Config{Listen: Listen{Addr: ":0", MaxConnAge: time.Nanosecond}}

	ingest := httptest.NewUnstartedServer(nil)
	ingest.Config = listener(cfg, ":0", boundConnections(okHandler(), cfg.MaxConnAge()))
	ingest.Config.Handler = boundConnections(okHandler(), cfg.MaxConnAge())
	ingest.Start()
	defer ingest.Close()
	if closed := get(t, ingest.Client(), ingest.URL); !closed {
		t.Error("the ingest listener did not recycle a connection past the age")
	}

	scrape := httptest.NewUnstartedServer(nil)
	scrape.Config = listener(cfg, ":0", okHandler())
	scrape.Config.Handler = okHandler()
	scrape.Start()
	defer scrape.Close()
	if closed := get(t, scrape.Client(), scrape.URL); closed {
		t.Error("the scrape endpoint recycles connections, which buys nothing " +
			"and costs a handshake per scrape")
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ok")
	})
}
